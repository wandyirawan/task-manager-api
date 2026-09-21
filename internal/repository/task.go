package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

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
// checked in Go (rec.ExpiresAt) rather than in SQL to avoid any SQLite
// datetime-format ambiguity.
func (r *taskRepository) LookupByKey(ctx context.Context, key, userID string) (*domain.IdempotencyRecord, error) {
	var rec domain.IdempotencyRecord
	err := r.db.GetContext(ctx, &rec,
		`SELECT key, user_id, task_id, response_status, response_body, created_at, expires_at
		   FROM idempotency_keys WHERE key = ? AND user_id = ?`, key, userID)
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

// CreateTaskWithKey is the idempotency SLOW PATH. The task and its idempotency
// key are inserted in ONE transaction (sqlx Beginx + deferred Rollback +
// explicit Commit), with lazy cleanup of this user's expired keys in the same
// transaction. The PK (key, user_id) UNIQUE constraint is the race backstop:
// if two requests race, the loser's key insert hits the constraint, the
// transaction rolls back (no zombie task), and we replay the winner's already
// committed snapshot with created=false.
func (r *taskRepository) CreateTaskWithKey(ctx context.Context, task *domain.Task, key string) (*domain.Task, bool, error) {
	// BEGIN IMMEDIATE via a dedicated *sql.Conn: acquire the SQLite write
	// lock AT BEGIN, not at the first INSERT. With a deferred BEGIN + bounded
	// pool, concurrent writers can each hold a read-locked connection while
	// waiting for the write lock — a self-made deadlock that surfaces as
	// SQLITE_BUSY after busy_timeout. IMMEDIATE serializes writers at BEGIN;
	// losers wait on busy_timeout (5s via DSN) and then take the replay path.
	// All statements (and COMMIT) must run on the SAME connection — that is
	// why we grab a single conn and drive raw SQL over it.
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	now := time.Now().UTC()

	// Lazy cleanup of expired keys for this user, inside the same transaction.
	if _, err := conn.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE user_id = ? AND expires_at < ?`,
		task.OwnerID, now); err != nil {
		return nil, false, err
	}

	// 1. Insert the task.
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO tasks (id, owner_id, assignee_id, title, description, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.OwnerID, task.AssigneeID, task.Title, task.Description, task.Status, task.CreatedAt, task.UpdatedAt); err != nil {
		return nil, false, err
	}

	// 2. Snapshot the task (json.Marshal(task) only) and insert the key.
	body, err := json.Marshal(task)
	if err != nil {
		return nil, false, err
	}

	if _, err := conn.ExecContext(ctx,
		`INSERT INTO idempotency_keys
		   (key, user_id, task_id, response_status, response_body, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		key, task.OwnerID, task.ID, 201, string(body), now, now.Add(idempotencyTTL)); err != nil {
		if isUniqueViolation(err) {
			// Race backstop: a concurrent writer already committed this key.
			// Our ROLLBACK (deferred) undoes our own task insert, then we
			// replay the committed snapshot.
			return r.replayByKey(ctx, key, task.OwnerID)
		}
		return nil, false, err
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, false, err
	}
	committed = true
	return task, true, nil
}

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint failure.
// Shared by user.go (email UNIQUE) and task.go (idempotency PK backstop).
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}

// replayByKey re-reads an already-committed idempotency key and decodes its
// stored snapshot. Used after a UNIQUE violation so callers get the original
// task rather than an error.
func (r *taskRepository) replayByKey(ctx context.Context, key, userID string) (*domain.Task, bool, error) {
	var rec domain.IdempotencyRecord
	err := r.db.GetContext(ctx, &rec,
		`SELECT key, user_id, task_id, response_status, response_body, created_at, expires_at
		   FROM idempotency_keys WHERE key = ? AND user_id = ?`, key, userID)
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
		 FROM tasks WHERE id = ? AND owner_id = ?`, taskID, ownerID)
	if err == sql.ErrNoRows {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// escapeLike escapes the SQLite LIKE wildcards so user search terms are
// matched literally while the surrounding % performs the partial match.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// listWhere builds the shared WHERE clause + args for List. The same clause
// drives both the row query and the COUNT so pagination meta is consistent.
func listWhere(f domain.TaskFilter) (string, []any) {
	conds := []string{"owner_id = ?"}
	args := []any{f.OwnerID}

	if f.Status.Valid() {
		conds = append(conds, "status = ?")
		args = append(args, string(f.Status))
	}
	if f.Search != "" {
		conds = append(conds, "title LIKE ? ESCAPE '\\'")
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
	query := "SELECT id, owner_id, assignee_id, title, description, status, created_at, updated_at" +
		" FROM tasks WHERE " + where +
		" ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?"
	rowsArgs := append(args, f.Limit, offset)

	tasks := []domain.Task{}
	if err := r.db.SelectContext(ctx, &tasks, query, rowsArgs...); err != nil {
		return nil, 0, err
	}

	return tasks, total, nil
}

func (r *taskRepository) Update(ctx context.Context, ownerID, taskID string, in domain.UpdateTaskInput) (*domain.Task, error) {
	sets := []string{"updated_at = ?"}
	args := []any{time.Now().UTC()}

	if in.Title != nil {
		sets = append(sets, "title = ?")
		args = append(args, *in.Title)
	}
	if in.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *in.Description)
	}
	if in.Status != nil {
		sets = append(sets, "status = ?")
		args = append(args, string(*in.Status))
	}
	args = append(args, taskID, ownerID)

	res, err := r.db.ExecContext(ctx,
		"UPDATE tasks SET "+strings.Join(sets, ", ")+" WHERE id = ? AND owner_id = ?", args...)
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
		 FROM tasks WHERE id = ? AND owner_id = ?`, taskID, ownerID)
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
		"DELETE FROM tasks WHERE id = ? AND owner_id = ?", taskID, ownerID)
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
