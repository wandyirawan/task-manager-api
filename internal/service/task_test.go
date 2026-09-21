package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/wandyirawan/task-manager-api/internal/domain"
)

type fakeRepo struct {
	tasks    []domain.Task
	createFn func(ctx context.Context, task *domain.Task) error
	// update applies only non-nil fields to mirror the real repo.
	updateFn func(ownerID, taskID string, in domain.UpdateTaskInput) (*domain.Task, error)
}

func (f *fakeRepo) seed(t *domain.Task) { f.tasks = append(f.tasks, *t) }

func (f *fakeRepo) Create(ctx context.Context, task *domain.Task) error {
	if f.createFn != nil {
		return f.createFn(ctx, task)
	}
	f.tasks = append(f.tasks, *task)
	return nil
}

func (f *fakeRepo) GetByID(ctx context.Context, ownerID, taskID string) (*domain.Task, error) {
	for i := range f.tasks {
		t := &f.tasks[i]
		if t.ID == taskID && t.OwnerID == ownerID {
			return t, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeRepo) List(ctx context.Context, filter domain.TaskFilter) ([]domain.Task, int, error) {
	var out []domain.Task
	for _, t := range f.tasks {
		if t.OwnerID != filter.OwnerID {
			continue
		}
		if filter.Status.Valid() && t.Status != filter.Status {
			continue
		}
		out = append(out, t)
	}
	return out, len(out), nil
}

func (f *fakeRepo) Update(ctx context.Context, ownerID, taskID string, in domain.UpdateTaskInput) (*domain.Task, error) {
	if f.updateFn != nil {
		return f.updateFn(ownerID, taskID, in)
	}
	for i := range f.tasks {
		t := &f.tasks[i]
		if t.ID == taskID && t.OwnerID == ownerID {
			if in.Title != nil {
				t.Title = *in.Title
			}
			if in.Description != nil {
				t.Description = *in.Description
			}
			if in.Status != nil {
				t.Status = *in.Status
			}
			cpy := *t
			return &cpy, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeRepo) Delete(ctx context.Context, ownerID, taskID string) error {
	for i := range f.tasks {
		if f.tasks[i].ID == taskID && f.tasks[i].OwnerID == ownerID {
			f.tasks = append(f.tasks[:i], f.tasks[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

func newService(repo TaskRepository) *TaskService {
	tr := slog.New(slog.NewTextHandler(nopWriter{}, nil))
	svc := NewTaskService(repo, tr)
	fixed := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixed }
	return svc
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestServiceCreateHappyPath(t *testing.T) {
	repo := &fakeRepo{}
	svc := newService(repo)

	task, err := svc.Create(context.Background(), "user-1", domain.CreateTaskInput{
		Title:       "Setup CI",
		Description: "add github actions",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if task.ID == "" {
		t.Error("ID should be set")
	}
	if task.OwnerID != "user-1" {
		t.Errorf("ownerID = %q, want user-1", task.OwnerID)
	}
	if task.Status != domain.TaskStatusTodo {
		t.Errorf("status = %q, want todo", task.Status)
	}
	want := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	if !task.CreatedAt.Equal(want) || !task.UpdatedAt.Equal(want) {
		t.Errorf("timestamps = %v/%v, want %v", task.CreatedAt, task.UpdatedAt, want)
	}
	if len(repo.tasks) != 1 {
		t.Errorf("repo has %d tasks, want 1", len(repo.tasks))
	}
}

func TestServiceCreateValidationError(t *testing.T) {
	svc := newService(&fakeRepo{})

	// Empty title → ErrValidation, no task persisted.
	_, err := svc.Create(context.Background(), "user-1", domain.CreateTaskInput{Title: "  "})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
}

func TestServiceGetNotFound(t *testing.T) {
	repo := &fakeRepo{}
	repo.seed(&domain.Task{ID: "t1", OwnerID: "user-1"})
	svc := newService(repo)

	_, err := svc.Get(context.Background(), "user-1", "missing")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestServiceUpdatePartialOnly(t *testing.T) {
	repo := &fakeRepo{}
	repo.seed(&domain.Task{
		ID: "t1", OwnerID: "user-1", Title: "old", Description: "keep", Status: domain.TaskStatusTodo,
	})
	svc := newService(repo)

	newTitle := "new title"
	newStatus := domain.TaskStatusDone
	updated, err := svc.Update(context.Background(), "user-1", "t1", domain.UpdateTaskInput{
		Title:  &newTitle,
		Status: &newStatus,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != newTitle || updated.Status != newStatus {
		t.Errorf("update mismatch: %+v", updated)
	}
	// Description untouched.
	if updated.Description != "keep" {
		t.Errorf("description = %q, want keep", updated.Description)
	}
	// Original (now-mutated) record still shows the partial change only.
	if repo.tasks[0].Description != "keep" {
		t.Errorf("repo description = %q, want keep", repo.tasks[0].Description)
	}
}

func TestServiceUpdateInvalidStatus(t *testing.T) {
	repo := &fakeRepo{}
	repo.seed(&domain.Task{ID: "t1", OwnerID: "user-1", Title: "x"})
	svc := newService(repo)

	bad := domain.TaskStatus("exploded")
	_, err := svc.Update(context.Background(), "user-1", "t1", domain.UpdateTaskInput{Status: &bad})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
}

func TestServiceDeleteNotFound(t *testing.T) {
	svc := newService(&fakeRepo{})
	if err := svc.Delete(context.Background(), "user-1", "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestServiceListNormalizesPagination(t *testing.T) {
	repo := &fakeRepo{}
	repo.seed(&domain.Task{ID: "t1", OwnerID: "user-1", Title: "a"})
	svc := newService(repo)

	// Page 0 / Limit 0 and huge limit must be clamped.
	tasks, total, err := svc.List(context.Background(), "user-1", domain.TaskFilter{Page: 0, Limit: 0})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(tasks) != 1 {
		t.Errorf("list total=%d n=%d, want 1/1", total, len(tasks))
	}
}
