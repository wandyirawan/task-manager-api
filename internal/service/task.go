package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/wandyirawan/task-manager-api/internal/domain"
)

// TaskRepository is the persistence contract the service consumes
// (consumer-side interface, implemented by internal/repository). Keeping it
// here lets the service layer be unit-tested against a hand-rolled fake.
type TaskRepository interface {
	Create(ctx context.Context, task *domain.Task) error
	GetByID(ctx context.Context, ownerID, taskID string) (*domain.Task, error)
	List(ctx context.Context, f domain.TaskFilter) ([]domain.Task, int, error)
	Update(ctx context.Context, ownerID, taskID string, in domain.UpdateTaskInput) (*domain.Task, error)
	Delete(ctx context.Context, ownerID, taskID string) error
	// Assign executes the transactional assign flow (update + log) inside one
	// SQL transaction and returns ErrForbidden when no matching row belongs to
	// ownerID.
	Assign(ctx context.Context, ownerID, taskID, assigneeID string) error
}

// IdempotencyStore is the atomic persistence contract for idempotent task
// creation (P5). It mirrors the consumer-side interface pattern: the service
// defines what it needs, the sqlx repository implements it, so the service
// stays unit-testable against a fake.
//
// LookupByKey is the fast path (read-only); a not-found or expired row returns
// (nil, nil) — NOT an error. CreateTaskWithKey is the slow path: it inserts the
// task and its idempotency key in ONE transaction, and on a PK (key,user_id)
// collision returns the already-stored task with created=false (the race
// backstop).
type IdempotencyStore interface {
	LookupByKey(ctx context.Context, key, userID string) (*domain.IdempotencyRecord, error)
	CreateTaskWithKey(ctx context.Context, task *domain.Task, key string) (*domain.Task, bool, error)
}

// Notifier is called OUTSIDE the assignment transaction to alert the assignee.
// Implementations can be log-only, email, webhook, etc. For tests a mock that
// just records calls is used.
type Notifier interface {
	Notify(ctx context.Context, taskID, assigneeID, actorID string) error
}

// noOpNotifier does nothing — useful when the caller doesn't want notifications.
type noOpNotifier struct{}

func (n noOpNotifier) Notify(context.Context, string, string, string) error { return nil }

// TaskService implements the task use-case layer. All operations are
// owner-scoped: the authenticated user's ID is threaded into every repository
// call so ownership is enforced at the query boundary, never in the handler.
type TaskService struct {
	repo   TaskRepository
	idem   IdempotencyStore
	logger *slog.Logger
	now    func() time.Time
	notf   Notifier
}

// NewTaskService builds a TaskService with the given repository and logger.
// Time is injectable via the constructor for deterministic tests.
//
// The sqlx-backed repository satisfies BOTH TaskRepository and IdempotencyStore
// (the atomic idempotent insert shares the same connection as the plain CRUD
// operations), so we recover the IdempotencyStore capability from the same
// repository object via the interface it also implements. Wiring stays in
// main.go untouched (composition root passes a single *taskRepository).
func NewTaskService(repo TaskRepository, logger *slog.Logger, notf Notifier) *TaskService {
	if notf == nil {
		notf = noOpNotifier{}
	}
	svc := &TaskService{
		repo:   repo,
		logger: logger,
		now:    time.Now,
		notf:   notf,
	}
	if idem, ok := repo.(IdempotencyStore); ok {
		svc.idem = idem
	}
	return svc
}

// taskID returns a fresh v4 UUID for a new task.
func (s *TaskService) taskID() string {
	return uuid.NewString()
}

// Create validates the input, then persists a new task owned by ownerID.
// Idempotency is handled by CreateIdempotent (P5) — this is the plain insert
// path kept for callers that don't need a key.
func (s *TaskService) Create(ctx context.Context, ownerID string, in domain.CreateTaskInput) (*domain.Task, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}

	now := s.now().UTC()
	t := &domain.Task{
		ID:          s.taskID(),
		OwnerID:     ownerID,
		Title:       in.Title,
		Description: in.Description,
		Status:      domain.TaskStatusTodo,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.repo.Create(ctx, t); err != nil {
		s.logger.Error("task create failed", "error", err)
		return nil, err
	}

	return t, nil
}

// CreateIdempotent creates a task idempotently: the same (key, userID) is
// guaranteed to produce the same task and an identical response, no matter how
// many times the request is retried.
//
// Flow:
//  1. validate input (errors short-circuit before any store call)
//  2. FAST PATH: LookupByKey — hit (not expired) → replay the stored snapshot,
//     created=false
//  3. SLOW PATH: build a fresh task → IdempotencyStore.CreateTaskWithKey (one
//     transaction: insert task + insert key) → created=true, or created=false if
//     a concurrent writer already won the (key,user_id) race.
//
// The snapshot stored is json.Marshal(task) only; the handler wraps it in the
// {"data": ...} envelope, so both paths emit byte-identical bytes.
func (s *TaskService) CreateIdempotent(ctx context.Context, userID, idemKey string, in domain.CreateTaskInput) (*domain.Task, bool, error) {
	if s.idem == nil {
		return nil, false, fmt.Errorf("idempotent create: idempotency store not configured")
	}

	if err := in.Validate(); err != nil {
		return nil, false, err
	}

	// FAST PATH — read-only replay.
	rec, err := s.idem.LookupByKey(ctx, idemKey, userID)
	if err != nil {
		return nil, false, fmt.Errorf("idempotent create: %w", err)
	}
	if rec != nil && rec.ExpiresAt.After(s.now()) {
		var t domain.Task
		if err := json.Unmarshal([]byte(rec.ResponseBody), &t); err != nil {
			return nil, false, fmt.Errorf("idempotent create: decode snapshot: %w", err)
		}
		return &t, false, nil // REPLAY
	}

	// SLOW PATH — atomic insert via the store.
	now := s.now().UTC()
	task := &domain.Task{
		ID:          s.taskID(),
		OwnerID:     userID,
		Title:       in.Title,
		Description: in.Description,
		Status:      domain.TaskStatusTodo,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	createdTask, created, err := s.idem.CreateTaskWithKey(ctx, task, idemKey)
	if err != nil {
		return nil, false, fmt.Errorf("idempotent create: %w", err)
	}
	return createdTask, created, nil
}

// Get returns one task owned by ownerID, or domain.ErrNotFound.
func (s *TaskService) Get(ctx context.Context, ownerID, taskID string) (*domain.Task, error) {
	t, err := s.repo.GetByID(ctx, ownerID, taskID)
	if err != nil {
		s.logger.Error("task get failed", "task_id", taskID, "error", err)
		return nil, err
	}
	return t, nil
}

// List returns a page of the caller's tasks plus the total matching count.
func (s *TaskService) List(ctx context.Context, ownerID string, f domain.TaskFilter) ([]domain.Task, int, error) {
	f.OwnerID = ownerID
	f = f.Normalize()
	return s.repo.List(ctx, f)
}

// Update validates the partial payload and applies the provided changes,
// returning the refreshed task. Returns domain.ErrNotFound when the task does
// not belong to the caller.
func (s *TaskService) Update(ctx context.Context, ownerID, taskID string, in domain.UpdateTaskInput) (*domain.Task, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}

	t, err := s.repo.Update(ctx, ownerID, taskID, in)
	if err != nil {
		s.logger.Error("task update failed", "task_id", taskID, "error", err)
		return nil, err
	}
	return t, nil
}

// Delete removes a task owned by ownerID. Returns domain.ErrNotFound when no
// such task exists.
func (s *TaskService) Delete(ctx context.Context, ownerID, taskID string) error {
	if err := s.repo.Delete(ctx, ownerID, taskID); err != nil {
		s.logger.Error("task delete failed", "task_id", taskID, "error", err)
		return err
	}
	return nil
}

// Assign moves a task to someone else. The whole operation runs inside a single
// database transaction: update assignee_id on the task and append a log entry
// to task_logs. After the tx commits the notifier gets called outside the
// transaction (failures do NOT undo the assignment). Owner check is done at
// the SQL layer (WHERE id=? AND owner_id=?), so non-owners cannot reassign.
func (s *TaskService) Assign(ctx context.Context, ownerID, taskID, assigneeID string) (*domain.Task, error) {
	if assigneeID == "" {
		return nil, domain.ErrValidation
	}

	if err := s.repo.Assign(ctx, ownerID, taskID, assigneeID); err != nil {
		return nil, err
	}

	// Notification happens OUTSIDE the transaction. Failures here do NOT undo
	// the assignment (the user already got the notification they asked for).
	if err := s.notf.Notify(ctx, taskID, assigneeID, ownerID); err != nil {
		s.logger.Warn("notify assignee failed", "task_id", taskID, "error", err)
	}

	return s.Get(ctx, ownerID, taskID)
}
