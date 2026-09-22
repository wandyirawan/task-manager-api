package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgerrcode"
	"github.com/jmoiron/sqlx"

	"github.com/wandyirawan/task-manager-api/internal/domain"
	"github.com/wandyirawan/task-manager-api/internal/service"
)

var _ service.TaskRepository = (*taskRepository)(nil)

// idempotencyTTL is the replay window for an idempotency key (SPEC §5: 24h,
// matching the JWT window). Hardcoded here; computed in Go (not SQL) so the
// expiry is deterministic for tests.
const idempotencyTTL = 24 * time.Hour

// idemInsert is the row shape for the idempotency_keys insert. Kept as a
// dedicated struct (rather than *domain.Task) because the columns differ from
// the tasks table and we control the snapshot body explicitly.
type idemInsert struct {
	Key            string    `db:"key"`
	UserID         string    `db:"user_id"`
	TaskID         string    `db:"task_id"`
	ResponseStatus int       `db:"response_status"`
	ResponseBody   string    `db:"response_body"`
	CreatedAt      time.Time `db:"created_at"`
	ExpiresAt      time.Time `db:"expires_at"`
}

type taskRepository struct {
	db *sqlx.DB
}

// NewTaskRepository wires the sqlx-backed task repository onto the shared DB.
func NewTaskRepository(db *sqlx.DB) service.TaskRepository {
	return &taskRepository{db: db}
}

func (r *taskRepository) Create(ctx context.Context, task *domain.Task) error {
	_, err := r.db.NamedExecContext(ctx,
		`INSERT INTO tasks (id, owner_id, assignee_id, title, description, status, created_at, updated_at)
		 VALUES (:id, :owner_id, :assignee_id, :title, :description, :status, :created_at, :updated_at)`,
		task)
	if err != nil {
		return err
	}
	return nil
}

// LookupByKey is the idempotency FAST PATH: a read-only fetch of a stored key
// for (key, userID). A not-found or already-expired row returns (nil, nil) —
// NOT an error — so the caller falls through to the slow path. Expiry is
// checked in Go (rec.ExpiresAt) rather than in SQL to keep the comparison
// deterministic across drivers.
func (r *taskRepository) LookupByKey(ctx context.Context, key, userID string) (*domain.IdempotencyRecord, error) {
	var rec domain.IdempotencyRecord
	err := r.db.GetContext(ctx, &rec,
		`SELECT key, user_id, task_id, response_status, response_body, created_at, expires_at
		   FROM idempotency_keys WHERE key = $1 AND user_id = $2`, key, userID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rec.ExpiresAt.Before(time.Now().UTC()) {
		// Expired: treat as not-found so a fresh request creates a new task.
		return nil, nil
	}
	return &rec, nil
}

// isBusy reports whether err is a transient PostgreSQL serialization failure
// (SQLSTATE 40001) or deadlock (40P01) — both safe to retry. Shared across
// idempotency, assign, and any future tx-writes that may hit contention under
// MVCC.
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgerrcode.SerializationFailure ||
			pgErr.Code == pgerrcode.DeadlockDetected
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "could not serialize access") ||
		strings.Contains(msg, "serialization_failure") ||
		strings.Contains(msg, "deadlock detected")
}

const maxRetries = 5

// CreateTaskWithKey is the idempotency SLOW PATH.  The task and its
// idempotency key are inserted in ONE transaction with lazy cleanup of
// expired keys in the same tx. The PK (key, user_id) UNIQUE constraint is
// the race backstop: if two goroutines race, the loser gets the UNIQUE
// violation, rolls back cleanly, and replays the winner's snapshot.
//
// Retry: transient serialization/deadlock errors are retried with exponential
// back-off (see maxRetries / sleepBackOff).  Permanent failures (UNIQUE
// collision, FK/integrity violations, etc.) are short-circuited immediately.
func (r *taskRepository) CreateTaskWithKey(ctx context.Context, task *domain.Task, key string) (*domain.Task, bool, error) {
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			sleepBackOff(attempt) // wait before retrying a transient conflict
		}
		t, created, err := r.doTx(ctx, task, key)
		if err != nil {
			if isBusy(err) {
				continue // retry
			}
			return nil, false, err // non-retryable → return immediately
		}
		return t, created, nil
	}
	return nil, false, fmt.Errorf("idempotent create: %w after %d retries", domain.ErrInternal, maxRetries)
}

// doTx executes one attempt: opens a transaction, runs the lazy-delete + task
// insert + key-insert + commit chain. On a UNIQUE violation it rolls back and
// replays the already-stored snapshot so the caller gets the original task.
func (r *taskRepository) doTx(ctx context.Context, task *domain.Task, key string) (*domain.Task, bool, error) {
	tx, err := r.db.Beginx()
	if err != nil {
		return nil, false, err
	}
	// Rollback is a no-op once Commit succeeds; it cleans up on any error path.
	defer tx.Rollback() //nolint:errcheck

	now := time.Now().UTC()

	// Lazy cleanup of expired keys for this user, inside the same transaction.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE user_id = $1 AND expires_at < $2`,
		task.OwnerID, now); err != nil {
		return nil, false, err
	}

	// 1. Insert the task (FK task_id→tasks means this must come before the key).
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tasks (id, owner_id, assignee_id, title, description, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		task.ID, task.OwnerID, task.AssigneeID, task.Title, task.Description, task.Status, task.CreatedAt, task.UpdatedAt); err != nil {
		if isUniqueViolation(err) {
			// A prior call with the same key/user_id already wrote this task.
			// Our rollback (deferred) cleans everything; replay the stored snapshot.
			return r.replayByKey(ctx, key, task.OwnerID)
		}
		return nil, false, err
	}

	// 2. Snapshot + insert idempotency key. UNIQUE on (key, user_id) is the
	// race backstop: if two goroutines race, the loser hits this constraint,
	// rolls back (no orphan key or task), and replays.
	body, err := json.Marshal(task)
	if err != nil {
		return nil, false, err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO idempotency_keys
		   (key, user_id, task_id, response_status, response_body, created_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		key, task.OwnerID, task.ID, 201, string(body), now, now.Add(idempotencyTTL)); err != nil {
		if isUniqueViolation(err) {
			// Concurrent writer won this key already — replay their snapshot.
			return r.replayByKey(ctx, key, task.OwnerID)
		}
		return nil, false, err
	}

	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return task, true, nil
}

// sleepBackOff pauses for an exponential-backoff duration based on attempt count.
func sleepBackOff(attempt int) {
	d := time.Duration(100*(1<<uint(attempt))) * time.Millisecond
	if d > 1*time.Second {
		d = 1 * time.Second
	}
	time.Sleep(d)
}

// isUniqueViolation reports whether err is a PostgreSQL UNIQUE violation
// (SQLSTATE 23505). Shared by user.go (email UNIQUE) and task.go (idempotency
// PK backstop).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgerrcode.UniqueViolation
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique violation") || strings.Contains(msg, "23505")
}

// replayByKey re-reads an already-committed idempotency key and decodes its
// stored snapshot. Used after a UNIQUE violation so callers get the original
// task rather than an error.
func (r *taskRepository) replayByKey(ctx context.Context, key, userID string) (*domain.Task, bool, error) {
	var rec domain.IdempotencyRecord
	err := r.db.GetContext(ctx, &rec,
		`SELECT key, user_id, task_id, response_status, response_body, created_at, expires_at
		   FROM idempotency_keys WHERE key = $1 AND user_id = $2`, key, userID)
	if err == sql.ErrNoRows {
		return nil, false, domain.ErrNotFound
	}
	if err != nil {
		return nil, false, err
	}
	var t domain.Task
	if err := json.Unmarshal([]byte(rec.ResponseBody), &t); err != nil {
		return nil, false, err
	}
	return &t, false, nil
}

func (r *taskRepository) GetByID(ctx context.Context, ownerID, taskID string) (*domain.Task, error) {
	var t domain.Task
	err := r.db.GetContext(ctx, &t,
		`SELECT id, owner_id, assignee_id, title, description, status, created_at, updated_at
		 FROM tasks WHERE id = $1 AND owner_id = $2`, taskID, ownerID)
	if err == sql.ErrNoRows {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// escapeLike escapes the LIKE wildcards so user search terms are matched
// literally while the surrounding % performs the partial match. The explicit
// ESCAPE '\' in the query tells Postgres which character is the escape.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// listWhere builds the shared WHERE clause + args for List. The same clause
// drives both the row query and the COUNT so pagination meta is consistent.
// Placeholder numbers are assigned positionally ($1, $2, …) because Postgres
// requires explicit ordinal placeholders (no `?`).
func listWhere(f domain.TaskFilter) (string, []any) {
	conds := []string{"owner_id = $1"}
	args := []any{f.OwnerID}

	if f.Status.Valid() {
		conds = append(conds, fmt.Sprintf("status = $%d", len(args)+1))
		args = append(args, string(f.Status))
	}
	if f.Search != "" {
		conds = append(conds, fmt.Sprintf("title LIKE $%d ESCAPE '\\'", len(args)+1))
		args = append(args, "%"+escapeLike(f.Search)+"%")
	}

	return strings.Join(conds, " AND "), args
}

func (r *taskRepository) List(ctx context.Context, f domain.TaskFilter) ([]domain.Task, int, error) {
	where, args := listWhere(f)

	var total int
	if err := r.db.GetContext(ctx, &total,
		"SELECT COUNT(*) FROM tasks WHERE "+where, args...); err != nil {
		return nil, 0, err
	}

	offset := (f.Page - 1) * f.Limit
	// LIMIT/OFFSET placeholders follow the WHERE args positionally.
	limitPos := len(args) + 1
	offsetPos := len(args) + 2
	query := "SELECT id, owner_id, assignee_id, title, description, status, created_at, updated_at" +
		" FROM tasks WHERE " + where +
		fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d", limitPos, offsetPos)
	rowsArgs := append(args, f.Limit, offset)

	tasks := []domain.Task{}
	if err := r.db.SelectContext(ctx, &tasks, query, rowsArgs...); err != nil {
		return nil, 0, err
	}

	return tasks, total, nil
}

func (r *taskRepository) Update(ctx context.Context, ownerID, taskID string, in domain.UpdateTaskInput) (*domain.Task, error) {
	sets := []string{"updated_at = $1"}
	args := []any{time.Now().UTC()}

	if in.Title != nil {
		sets = append(sets, fmt.Sprintf("title = $%d", len(args)+1))
		args = append(args, *in.Title)
	}
	if in.Description != nil {
		sets = append(sets, fmt.Sprintf("description = $%d", len(args)+1))
		args = append(args, *in.Description)
	}
	if in.Status != nil {
		sets = append(sets, fmt.Sprintf("status = $%d", len(args)+1))
		args = append(args, string(*in.Status))
	}

	// WHERE id = $N AND owner_id = $N+1 follow the SET args.
	idPos := len(args) + 1
	ownerPos := len(args) + 2
	args = append(args, taskID, ownerID)

	res, err := r.db.ExecContext(ctx,
		fmt.Sprintf("UPDATE tasks SET %s WHERE id = $%d AND owner_id = $%d",
			strings.Join(sets, ", "), idPos, ownerPos), args...)
	if err != nil {
		return nil, err
	}

	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, domain.ErrNotFound
	}

	// Return the freshly updated row.
	var t domain.Task
	err = r.db.GetContext(ctx, &t,
		`SELECT id, owner_id, assignee_id, title, description, status, created_at, updated_at
		 FROM tasks WHERE id = $1 AND owner_id = $2`, taskID, ownerID)
	if err == sql.ErrNoRows {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *taskRepository) Delete(ctx context.Context, ownerID, taskID string) error {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM tasks WHERE id = $1 AND owner_id = $2", taskID, ownerID)
	if err != nil {
		return err
	}

	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}
