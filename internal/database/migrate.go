// Package database applies schema migrations. Migrations run on server start
// as well as via `make migrate`, so a fresh clone reaches a working state
// without a separate setup step.
package database

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/vivianobiako/qless/api/migrations"
)

// migrateLockKey is an arbitrary, fixed advisory-lock id. Anything migrating
// this database takes it first, so two of them cannot both find the schema
// empty and both try to build it: the test packages run in parallel against
// one database, and a deploy that scales past one instance would too.
const migrateLockKey = 7311_2023

func Migrate(databaseURL string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database for migration: %w", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	// A session-level lock lives on one connection, so the connection is
	// held for the duration; goose works through the pool beside it.
	lock, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open connection for migration lock: %w", err)
	}
	defer func() { _ = lock.Close() }()

	if _, err := lock.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrateLockKey); err != nil {
		return fmt.Errorf("take migration lock: %w", err)
	}
	defer func() { _, _ = lock.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", migrateLockKey) }()

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
