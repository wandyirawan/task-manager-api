package service

import (
	"context"
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
}

// TaskService implements the task use-case layer. All operations are
// owner-scoped: the authenticated user's ID is threaded into every repository
// call so ownership is enforced at the query boundary, never in the handler.
type TaskService struct {
	repo   TaskRepository
	logger *slog.Logger
	now    func() time.Time
}

// NewTaskService builds a TaskService with the given repository and logger.
// Time is injectable via the constructor for deterministic tests.
func NewTaskService(repo TaskRepository, logger *slog.Logger) *TaskService {
	return &TaskService{
		repo:   repo,
		logger: logger,
		now:    time.Now,
	}
}

// taskID returns a fresh v4 UUID for a new task.
func (s *TaskService) taskID() string {
	return uuid.NewString()
}

// Create validates the input, then persists a new task owned by ownerID.
// Idempotency is out of scope here (P5) — this is a plain insert.
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
