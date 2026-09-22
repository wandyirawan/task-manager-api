package infra

import (
	"context"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	pgx5 "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jmoiron/sqlx"

	"github.com/wandyirawan/task-manager-api/migrations"
)

// RunMigrations applies pending SQL migrations using the SAME pooled *sql.DB.
// Migrations are embedded in the binary — see migrations/migrations.go — so
// runtime never depends on external files or a migrate CLI. Idempotent:
// golang-migrate no-ops when already at the latest version. Fail-fast
// contract: main exits if this returns an error.
//
// The migrate driver runs against the same *sql.DB the app uses; we do NOT
// call m.Close() because that would close the shared pool. The driver's
// dedicated connection is released when the application's *sql.DB is closed
// (at process exit / test cleanup).
func RunMigrations(db *sqlx.DB) error {
	// golang-migrate's pgx/v5 driver requires the database name up front, so
	// we read it from the live connection.
	var dbName string
	if err := db.GetContext(context.Background(), &dbName, "SELECT current_database()"); err != nil {
		return fmt.Errorf("detect database name: %w", err)
	}

	pgxDrv, err := pgx5.WithInstance(db.DB, &pgx5.Config{
		DatabaseName:          dbName,
		MultiStatementEnabled: true, // run the multi-statement up/down files safely
	})
	if err != nil {
		return fmt.Errorf("migrate driver: %w", err)
	}

	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("migrate source: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "pgx5", pgxDrv)
	if err != nil {
		return fmt.Errorf("migrate instance: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate up: %w", err)
	}

	// Detach ONLY the file source. Do NOT call m.Close(): it would close the
	// underlying database instance shared with our connection pool.
	if err := src.Close(); err != nil {
		return fmt.Errorf("migrate source close: %w", err)
	}
	return nil
}
