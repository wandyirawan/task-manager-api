package infra_test

import (
	"context"
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
	"github.com/wandyirawan/task-manager-api/internal/infra"
)

// adminDB is the always-present maintenance database used to issue
// CREATE/DROP DATABASE for the throwaway test database.
const adminDB = "postgres"

var (
	infraTestDB    *sqlx.DB
	infraTestName  string
	infraTestReady bool
	infraTestErr   error
)

func TestMain(m *testing.M) {
	os.Exit(runInfraTests(m))
}

func runInfraTests(m *testing.M) int {
	infraTestDB, infraTestName, infraTestErr = setupInfraTestDB()
	infraTestReady = infraTestErr == nil
	if !infraTestReady {
		fmt.Fprintln(os.Stderr, "TEST_DB_URL unreachable, infra tests will skip:", infraTestErr)
	}
	code := m.Run()
	if infraTestReady {
		if infraTestDB != nil {
			infraTestDB.Close()
		}
		dropInfraTestDB(infraTestName)
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

func setupInfraTestDB() (*sqlx.DB, string, error) {
	base := testDBURL()

	adm, err := sqlx.Connect("pgx", swapDBName(base, adminDB))
	if err != nil {
		return nil, "", err
	}
	defer adm.Close()

	name := fmt.Sprintf("tmapi_infra_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := adm.ExecContext(context.Background(), `CREATE DATABASE "`+name+`"`); err != nil {
		return nil, "", err
	}

	cfg := &config.Config{
		DBURL:          swapDBName(base, name),
		DBMaxOpenConns: 4,
		DBMaxIdleConns: 4,
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

func dropInfraTestDB(name string) {
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
	m, err := migrate.NewWithDatabaseInstance("file://"+findMigrationsDir(), "postgres", pgDrv)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}
	return nil
}

func findMigrationsDir() string {
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

func TestNewDB(t *testing.T) {
	if !infraTestReady {
		t.Skipf("TEST_DB_URL unreachable: %v", infraTestErr)
	}

	if err := infraTestDB.Ping(); err != nil {
		t.Fatalf("db.Ping() failed: %v", err)
	}

	// Postgres identity check: current_database() resolves to our throwaway DB.
	var cur string
	if err := infraTestDB.GetContext(context.Background(), &cur, "SELECT current_database()"); err != nil {
		t.Fatalf("current_database: %v", err)
	}
	if cur != infraTestName {
		t.Errorf("current_database = %q, want %q", cur, infraTestName)
	}
}

// TestRunMigrationAndSchema runs the real golang-migrate up migration against
// the throwaway Postgres database and verifies the schema_migrations table
// plus all four domain tables; then confirms the down migration drops them.
func TestRunMigrationAndSchema(t *testing.T) {
	if !infraTestReady {
		t.Skipf("TEST_DB_URL unreachable: %v", infraTestErr)
	}
	db := infraTestDB

	// 1) migrate's own version table must be present.
	hasTable(t, db, "schema_migrations")

	// 2) All four domain tables from SPEC §4 must exist.
	expected := []string{"idempotency_keys", "task_logs", "tasks", "users"}
	for _, name := range expected {
		hasTable(t, db, name)
	}

	// 3) Reverse order DROP (down) must clean up cleanly.
	pgDrv, err := postgres.WithInstance(db.DB, &postgres.Config{})
	if err != nil {
		t.Fatalf("postgres.WithInstance: %v", err)
	}
	m, err := migrate.NewWithDatabaseInstance("file://"+findMigrationsDir(), "postgres", pgDrv)
	if err != nil {
		t.Fatalf("migrate.NewWithDatabaseInstance: %v", err)
	}
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
	var exists bool
	err := db.GetContext(context.Background(), &exists,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`, name)
	if err != nil {
		return false
	}
	return exists
}
