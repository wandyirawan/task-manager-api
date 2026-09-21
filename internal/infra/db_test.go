package infra_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	sqliteMigrate "github.com/golang-migrate/migrate/v4/database/sqlite"
	_ "github.com/golang-migrate/migrate/v4/source/file" // registers "file" source driver
	"github.com/jmoiron/sqlx"

	"github.com/wandyirawan/task-manager-api/internal/config"
	"github.com/wandyirawan/task-manager-api/internal/infra"
)

func testConfig(dir, name string) *config.Config {
	return &config.Config{
		DBPath:         filepath.Join(dir, name),
		DBMaxOpenConns: 4,
		DBMaxIdleConns: 4,
	}
}

func TestNewDB(t *testing.T) {
	cfg := testConfig(t.TempDir(), "test.db")

	db, err := infra.NewDB(cfg)
	if err != nil {
		t.Fatalf("NewDB failed: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping() failed: %v", err)
	}
}

func TestPRAGMAsViaDSN(t *testing.T) {
	cfg := testConfig(t.TempDir(), "pragma_test.db")

	db, err := infra.NewDB(cfg)
	if err != nil {
		t.Fatalf("NewDB failed: %v", err)
	}
	defer db.Close()

	// foreign_keys pragma must be ON via DSN.
	var fk int
	err = db.GetContext(context.Background(), &fk, "PRAGMA foreign_keys")
	if err != nil {
		t.Fatalf("query PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}

	// WAL journal mode must be active.
	var mode string
	err = db.GetContext(context.Background(), &mode, "PRAGMA journal_mode")
	if err != nil {
		t.Fatalf("query PRAGMA journal_mode: %v", err)
	}
	if !strings.Contains(strings.ToLower(mode), "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func findMigrationsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	// go test runs with cwd = package dir; walk up to the module root to
	// locate migrations/ regardless of where the suite was invoked from.
	for {
		cand := filepath.Join(dir, "migrations")
		if st, statErr := os.Stat(cand); statErr == nil && st.IsDir() {
			return cand
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("migrations/ directory not found walking up from " + dir)
		}
		dir = parent
	}
}

// TestRunMigrationAndSchema runs the real golang-migrate up migration against a
// temp DB and verifies the schema_migrations table plus all four domain tables.
func TestRunMigrationAndSchema(t *testing.T) {
	cfg := testConfig(t.TempDir(), "migrate_test.db")

	db, err := infra.NewDB(cfg)
	if err != nil {
		t.Fatalf("NewDB failed: %v", err)
	}
	defer db.Close()

	// Wire the migrate database driver onto the SAME pooled *sql.DB so the
	// migration runs against the identical connection/pragmas (WAL etc.).
	sqliteDrv, err := sqliteMigrate.WithInstance(db.DB, &sqliteMigrate.Config{})
	if err != nil {
		t.Fatalf("sqlite.WithInstance: %v", err)
	}

	migDir := findMigrationsDir(t)
	m, err := migrate.NewWithDatabaseInstance("file://"+migDir, "sqlite", sqliteDrv)
	if err != nil {
		t.Fatalf("migrate.NewWithDatabaseInstance: %v", err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("migrate Up: %v", err)
	}

	// 1) migrate's own version table must be present.
	hasTable(t, db, "schema_migrations")

	// 2) All four domain tables from SPEC §4 must exist.
	expected := []string{"idempotency_keys", "task_logs", "tasks", "users"}
	for _, name := range expected {
		hasTable(t, db, name)
	}

	// 3) Reverse order DROP (down) must clean up cleanly.
	if err := m.Down(); err != nil {
		t.Fatalf("migrate Down: %v", err)
	}
	for _, name := range expected {
		if tableExists(db, name) {
			t.Errorf("table %q still exists after Down", name)
		}
	}
}

func hasTable(t *testing.T, db *sqlx.DB, name string) {
	if !tableExists(db, name) {
		t.Errorf("table %q not found in schema", name)
		return
	}
	t.Logf("table %q exists", name)
}

func tableExists(db *sqlx.DB, name string) bool {
	rows, err := db.QueryContext(context.Background(),
		"SELECT name FROM sqlite_master WHERE type='table' AND name = ?", name)
	if err != nil {
		return false
	}
	defer rows.Close()

	exists := false
	for rows.Next() {
		exists = true
	}
	return exists
}