package repository

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wandyirawan/task-manager-api/internal/domain"
	"github.com/wandyirawan/task-manager-api/internal/service"
)

var _ service.TaskRepository = (*taskRepository)(nil)

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
