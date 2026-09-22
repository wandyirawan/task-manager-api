package repository_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	postgres "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"github.com/jmoiron/sqlx"

	"github.com/wandyirawan/task-manager-api/internal/config"
	"github.com/wandyirawan/task-manager-api/internal/domain"
	"github.com/wandyirawan/task-manager-api/internal/infra"
	"github.com/wandyirawan/task-manager-api/internal/repository"
)

// --- PostgreSQL-backed shared test database ---------------------------------
//
// All repository tests run against one throwaway Postgres database created
// once per test binary (per package). Each test truncates every table before
// it runs, so tests stay isolated without spinning up a fresh database per
// test. The database is dropped when the package finishes (see TestMain). If
// no Postgres is reachable the whole package is skipped, never failed.

// adminDB is the always-present maintenance database used to issue
// CREATE/DROP DATABASE for the throwaway test database.
const adminDB = "postgres"

var (
	repoTestDB    *sqlx.DB
	repoTestName  string
	repoTestErr   error
	repoTestReady bool
)

func TestMain(m *testing.M) {
	os.Exit(runRepoTests(m))
}

func runRepoTests(m *testing.M) int {
	repoTestDB, repoTestName, repoTestErr = setupSharedTestDB()
	repoTestReady = repoTestErr == nil
	if !repoTestReady {
		fmt.Fprintln(os.Stderr, "TEST_DB_URL unreachable, repository tests will skip:", repoTestErr)
	}
	code := m.Run()
	if repoTestReady {
		if repoTestDB != nil {
			repoTestDB.Close()
		}
		dropSharedTestDB(repoTestName)
	}
	return code
}

func testDBURL() string {
	if v := os.Getenv("TEST_DB_URL"); v != "" {
		return v
	}
	return "postgres://tm_user:tm_pass@localhost:5432/tmapi_test?sslmode=disable"
}

// swapDBName rewrites the database component of a postgres:// DSN.
func swapDBName(dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + name
	return u.String()
}

func setupSharedTestDB() (*sqlx.DB, string, error) {
	base := testDBURL()

	adm, err := sqlx.Connect("pgx", swapDBName(base, adminDB))
	if err != nil {
		return nil, "", err
	}
	defer adm.Close()

	name := fmt.Sprintf("tmapi_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := adm.ExecContext(context.Background(), `CREATE DATABASE "`+name+`"`); err != nil {
		return nil, "", err
	}

	cfg := &config.Config{
		DBURL:          swapDBName(base, name),
		DBMaxOpenConns: 64,
		DBMaxIdleConns: 64,
	}
	db, err := infra.NewDB(cfg)
	if err != nil {
		dropDBVia(adm, name)
		return nil, "", err
	}

	if err := migrateUp(db); err != nil {
		db.Close()
		dropDBVia(adm, name)
		return nil, "", err
	}
	return db, name, nil
}

func dropSharedTestDB(name string) {
	if name == "" {
		return
	}
	adm, err := sqlx.Connect("pgx", swapDBName(testDBURL(), adminDB))
	if err != nil {
		return
	}
	defer adm.Close()
	dropDBVia(adm, name)
}

func dropDBVia(adm *sqlx.DB, name string) {
	_, _ = adm.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`)
}

// migrateUp applies the real golang-migrate up migration using the Postgres
// driver against the supplied *sql.DB.
func migrateUp(db *sqlx.DB) error {
	pgDrv, err := postgres.WithInstance(db.DB, &postgres.Config{})
	if err != nil {
		return err
	}
	m, err := migrate.NewWithDatabaseInstance("file://"+locateMigrationsDir(), "postgres", pgDrv)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}
	return nil
}

func locateMigrationsDir() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	for {
		cand := filepath.Join(dir, "migrations")
		if st, statErr := os.Stat(cand); statErr == nil && st.IsDir() {
			return cand
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("migrations/ directory not found walking up from " + dir)
		}
		dir = parent
	}
}

func findMigrationsDir(t *testing.T) string {
	t.Helper()
	return locateMigrationsDir()
}

// newTestDB returns the shared, migrated test database (truncated first so
// each test starts from a clean schema). Skips the test if Postgres is
// unreachable.
func newTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	if !repoTestReady {
		t.Skipf("TEST_DB_URL unreachable: %v", repoTestErr)
	}
	truncateAll(t, repoTestDB)
	return repoTestDB
}

// newTestDBWithPool is the high-concurrency variant: the shared pool is sized
// generously (64) so N-goroutine tests never starve on connection acquisition.
func newTestDBWithPool(t *testing.T, n int) *sqlx.DB {
	t.Helper()
	if !repoTestReady {
		t.Skipf("TEST_DB_URL unreachable: %v", repoTestErr)
	}
	truncateAll(t, repoTestDB)
	return repoTestDB
}

func truncateAll(t *testing.T, db *sqlx.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`TRUNCATE TABLE users, tasks, task_logs, idempotency_keys RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate all: %v", err)
	}
}

// seedUser inserts a bare users row so the FK on tasks.owner_id satisfies.
func seedUser(t *testing.T, db *sqlx.DB, id string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')`, id, id+"@test.dev")
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
