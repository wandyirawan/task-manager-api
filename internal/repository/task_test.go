package repository_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	sqliteMigrate "github.com/golang-migrate/migrate/v4/database/sqlite"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"github.com/jmoiron/sqlx"

	"github.com/wandyirawan/task-manager-api/internal/config"
	"github.com/wandyirawan/task-manager-api/internal/domain"
	"github.com/wandyirawan/task-manager-api/internal/infra"
	"github.com/wandyirawan/task-manager-api/internal/repository"
)

func newTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	cfg := &config.Config{
		DBPath:         filepath.Join(t.TempDir(), "tasks_test.db"),
		DBMaxOpenConns: 4,
		DBMaxIdleConns: 4,
	}
	db, err := infra.NewDB(cfg)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	sqliteDrv, err := sqliteMigrate.WithInstance(db.DB, &sqliteMigrate.Config{})
	if err != nil {
		t.Fatalf("sqlite.WithInstance: %v", err)
	}
	m, err := migrate.NewWithDatabaseInstance("file://"+findMigrationsDir(t), "sqlite", sqliteDrv)
	if err != nil {
		t.Fatalf("migrate.NewWithDatabaseInstance: %v", err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("migrate Up: %v", err)
	}
	return db
}

func findMigrationsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		cand := filepath.Join(dir, "migrations")
		if st, statErr := os.Stat(cand); statErr == nil && st.IsDir() {
			return cand
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("migrations/ not found walking up from %s", dir)
		}
		dir = parent
	}
}

// seedUser inserts a bare users row so the FK on tasks.owner_id satisfies.
func seedUser(t *testing.T, db *sqlx.DB, id string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		"INSERT INTO users (id, email, password_hash) VALUES (?, ?, 'x')", id, id+"@test.dev")
	if err != nil {
		t.Fatalf("seed user %s: %v", id, err)
	}
}

func makeTask(ownerID, title string, status domain.TaskStatus) *domain.Task {
	now := time.Now().UTC()
	return &domain.Task{
		ID:          "tsk-" + title,
		OwnerID:     ownerID,
		Title:       title,
		Description: "desc of " + title,
		Status:      status,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func TestCreateRoundtripAndGetByID(t *testing.T) {
	h := newTestDB(t)
	seedUser(t, h, "user-a")
	repo := repository.NewTaskRepository(h)

	tsk := makeTask("user-a", "roundtrip", domain.TaskStatusTodo)
	if err := repo.Create(context.Background(), tsk); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(context.Background(), "user-a", tsk.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Title != tsk.Title || got.Status != domain.TaskStatusTodo {
		t.Errorf("roundtrip mismatch: got %+v", got)
	}
	if got.OwnerID != "user-a" {
		t.Errorf("ownerID = %q, want user-a", got.OwnerID)
	}
}

func TestListFilterStatusAndSearch(t *testing.T) {
	h := newTestDB(t)
	seedUser(t, h, "user-f")
	repo := repository.NewTaskRepository(h)
	ctx := context.Background()

	for _, tk := range []*domain.Task{
		makeTask("user-f", "alpha report", domain.TaskStatusTodo),
		makeTask("user-f", "beta report", domain.TaskStatusDone),
		makeTask("user-f", "gamma notes", domain.TaskStatusTodo),
	} {
		if err := repo.Create(ctx, tk); err != nil {
			t.Fatalf("Create %s: %v", tk.Title, err)
		}
	}

	// Filter by status.
	byStatus, total, err := repo.List(ctx, domain.TaskFilter{OwnerID: "user-f", Status: domain.TaskStatusDone, Page: 1, Limit: 10})
	if err != nil {
		t.Fatalf("List status: %v", err)
	}
	if total != 1 || len(byStatus) != 1 || byStatus[0].Title != "beta report" {
		t.Errorf("status filter: total=%d n=%d", total, len(byStatus))
	}

	// Search by title (case-insensitive partial, matches both "report").
	bySearch, total, err := repo.List(ctx, domain.TaskFilter{OwnerID: "user-f", Search: "report", Page: 1, Limit: 10})
	if err != nil {
		t.Fatalf("List search: %v", err)
	}
	if total != 2 || len(bySearch) != 2 {
		t.Errorf("search: total=%d n=%d, want 2/2", total, len(bySearch))
	}

	// LIKE escaping: a % should not act as a wildcard.
	makeSpecial, _, err := repo.List(ctx, domain.TaskFilter{OwnerID: "user-f", Search: "100%done", Page: 1, Limit: 10})
	if err != nil {
		t.Fatalf("List escape: %v", err)
	}
	if len(makeSpecial) != 0 {
		t.Errorf("unescaped %% matched %d rows, want 0", len(makeSpecial))
	}
}

func TestListPaginationMetaTotal(t *testing.T) {
	h := newTestDB(t)
	seedUser(t, h, "user-p")
	repo := repository.NewTaskRepository(h)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		tk := makeTask("user-p", "page item", domain.TaskStatusTodo)
		tk.ID = "page-" + string(rune('a'+i))
		if err := repo.Create(ctx, tk); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	// Page 1, limit 2 → 2 rows, total 5.
	page1, total, err := repo.List(ctx, domain.TaskFilter{OwnerID: "user-p", Page: 1, Limit: 2})
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if total != 5 || len(page1) != 2 {
		t.Errorf("page1: total=%d n=%d, want 5/2", total, len(page1))
	}

	// Page 3, limit 2 → 1 remaining row, total still 5.
	page3, total, err := repo.List(ctx, domain.TaskFilter{OwnerID: "user-p", Page: 3, Limit: 2})
	if err != nil {
		t.Fatalf("List page3: %v", err)
	}
	if total != 5 || len(page3) != 1 {
		t.Errorf("page3: total=%d n=%d, want 5/1", total, len(page3))
	}

	// Ordering: newest created_at first. All share time.Now; rely only on count.
	if len(page1) != 2 {
		t.Errorf("page1 should have 2 rows")
	}
}

func TestOwnerIsolation(t *testing.T) {
	h := newTestDB(t)
	seedUser(t, h, "owner-a")
	seedUser(t, h, "owner-b")
	repo := repository.NewTaskRepository(h)
	ctx := context.Background()

	tsk := makeTask("owner-a", "private task", domain.TaskStatusTodo)
	if err := repo.Create(ctx, tsk); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Owner B must not see owner A's task via GetByID.
	if _, err := repo.GetByID(ctx, "owner-b", tsk.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetByID cross-owner err=%v, want ErrNotFound", err)
	}

	// Owner B's List must not include owner A's task.
	list, total, err := repo.List(ctx, domain.TaskFilter{OwnerID: "owner-b"})
	if err != nil {
		t.Fatalf("List owner-b: %v", err)
	}
	if total != 0 || len(list) != 0 {
		t.Errorf("owner-b sees %d tasks (total=%d), want 0", len(list), total)
	}
}

func TestUpdatePartialAndDelete(t *testing.T) {
	h := newTestDB(t)
	seedUser(t, h, "user-u")
	repo := repository.NewTaskRepository(h)
	ctx := context.Background()

	tsk := makeTask("user-u", "original", domain.TaskStatusTodo)
	if err := repo.Create(ctx, tsk); err != nil {
		t.Fatalf("Create: %v", err)
	}

	newTitle := "renamed"
	newStatus := domain.TaskStatusDone
	updated, err := repo.Update(ctx, "user-u", tsk.ID, domain.UpdateTaskInput{
		Title:  &newTitle,
		Status: &newStatus,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != newTitle || updated.Status != newStatus {
		t.Errorf("update not applied: %+v", updated)
	}
	// Description left nil → unchanged.
	if updated.Description != tsk.Description {
		t.Errorf("description changed unexpectedly: %q", updated.Description)
	}

	// Delete then confirm gone.
	if err := repo.Delete(ctx, "user-u", tsk.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.GetByID(ctx, "user-u", tsk.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get after delete err=%v, want ErrNotFound", err)
	}

	// Update/Delete on missing id → ErrNotFound.
	if _, err := repo.Update(ctx, "user-u", "nope", domain.UpdateTaskInput{Title: &newTitle}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Update missing err=%v, want ErrNotFound", err)
	}
	if err := repo.Delete(ctx, "user-u", "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Delete missing err=%v, want ErrNotFound", err)
	}
}
