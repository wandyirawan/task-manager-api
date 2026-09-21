package infra

import (
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	sqliteMigrate "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jmoiron/sqlx"

	"github.com/wandyirawan/task-manager-api/migrations"
)

// RunMigrations applies pending SQL migrations using the SAME pooled *sql.DB
// (pragmas included). Migrations are embedded in the binary — see
// migrations/migrations.go — so runtime never depends on external files or a
// migrate CLI. Idempotent: golang-migrate no-ops when already at the latest
// version. Fail-fast contract: main exits if this returns an error.
func RunMigrations(db *sqlx.DB) error {
	sqliteDrv, err := sqliteMigrate.WithInstance(db.DB, &sqliteMigrate.Config{})
	if err != nil {
		return fmt.Errorf("migrate driver: %w", err)
	}

	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("migrate source: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "sqlite", sqliteDrv)
	if err != nil {
		return fmt.Errorf("migrate instance: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate up: %w", err)
	}

	// Detach ONLY the file source. Do NOT call m.Close(): it would close the
	// underlying sqlite database instance shared with our connection pool.
	if err := src.Close(); err != nil {
		return fmt.Errorf("migrate source close: %w", err)
	}
	return nil
}