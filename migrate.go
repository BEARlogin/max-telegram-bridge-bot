package main

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/sqlite/*.sql
var sqliteMigrationsFS embed.FS

//go:embed migrations/postgres/*.sql
var postgresMigrationsFS embed.FS

func runMigrations(db *sql.DB, driver string) error {
	var (
		sourceFS   fs.FS
		subdir     string
		driverName string
	)

	switch driver {
	case "sqlite3":
		sourceFS = sqliteMigrationsFS
		subdir = "migrations/sqlite"
		driverName = "sqlite3"
	case "postgres":
		sourceFS = postgresMigrationsFS
		subdir = "migrations/postgres"
		driverName = "postgres"
	default:
		return fmt.Errorf("unsupported migration driver: %s", driver)
	}

	// Existing DB compatibility: if tables exist but schema_migrations doesn't,
	// force version to 2 (current full state) so migrate doesn't re-apply.
	if err := maybeForceVersion(db, driver); err != nil {
		return fmt.Errorf("force version check: %w", err)
	}

	source, err := iofs.New(sourceFS, subdir)
	if err != nil {
		return fmt.Errorf("iofs source: %w", err)
	}

	var dbDriver database.Driver
	switch driverName {
	case "sqlite3":
		dbDriver, err = sqlite3.WithInstance(db, &sqlite3.Config{})
	case "postgres":
		dbDriver, err = postgres.WithInstance(db, &postgres.Config{})
	}
	if err != nil {
		return fmt.Errorf("migrate db driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", source, driverName, dbDriver)
	if err != nil {
		return fmt.Errorf("migrate instance: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}

	log.Println("migrations: up to date")
	return nil
}

// maybeForceVersion checks if the DB already has application tables but no
// schema_migrations table. In that case it creates schema_migrations and sets
// version=2 (dirty=false) so golang-migrate won't try to re-apply old migrations.
func maybeForceVersion(db *sql.DB, driver string) error {
	hasSchemaTbl := tableExists(db, driver, "schema_migrations")
	if hasSchemaTbl {
		return nil // already managed by migrate
	}

	hasPairs := tableExists(db, driver, "pairs")
	if !hasPairs {
		return nil // fresh DB, let migrate handle everything
	}

	// Existing DB without schema_migrations — force to version 2.
	log.Println("migrations: existing DB detected, forcing version to 2")

	switch driver {
	case "sqlite3":
		_, err := db.Exec(`
			CREATE TABLE IF NOT EXISTS schema_migrations (version uint64 not null primary key, dirty boolean not null);
			INSERT INTO schema_migrations (version, dirty) VALUES (2, false);
		`)
		return err
	case "postgres":
		_, err := db.Exec(`
			CREATE TABLE IF NOT EXISTS schema_migrations (version bigint not null primary key, dirty boolean not null);
			INSERT INTO schema_migrations (version, dirty) VALUES (2, false);
		`)
		return err
	}
	return nil
}

func tableExists(db *sql.DB, driver, table string) bool {
	var n int
	switch driver {
	case "sqlite3":
		err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n)
		return err == nil && n > 0
	case "postgres":
		err := db.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_name=$1", table).Scan(&n)
		return err == nil && n > 0
	}
	return false
}
