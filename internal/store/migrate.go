package store

import (
	"context"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zoowen/gateway/migrations"
)

// RunMigrations applies every .sql file in migrations/, in filename order,
// skipping ones already recorded in schema_migrations. Tracking versions
// (rather than just re-running everything, like 001 alone could get away
// with) is required as soon as a migration isn't idempotent — 002 does an
// ALTER TABLE ADD CONSTRAINT, which errors on a second run. Each migration
// runs in its own transaction together with the INSERT that records it, so
// a failure can't leave a migration half-applied-but-marked-done or
// applied-but-unmarked.
//
// This also self-heals a database that already had 001 applied before
// schema_migrations existed: 001 isn't in the tracking table yet, so it
// runs again — harmlessly, since it's CREATE TABLE IF NOT EXISTS — and
// only then gets recorded.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text primary key,
			applied_at timestamptz not null default now()
		)`); err != nil {
		return fmt.Errorf("store: creating schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("store: reading migrations: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var applied bool
		err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name).Scan(&applied)
		if err != nil {
			return fmt.Errorf("store: checking migration %s: %w", name, err)
		}
		if applied {
			continue
		}

		if err := applyMigration(ctx, pool, name); err != nil {
			return err
		}
	}

	return nil
}

func applyMigration(ctx context.Context, pool *pgxpool.Pool, name string) error {
	sqlBytes, err := migrations.FS.ReadFile(name)
	if err != nil {
		return fmt.Errorf("store: reading migration %s: %w", name, err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: starting transaction for migration %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
		return fmt.Errorf("store: applying migration %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
		return fmt.Errorf("store: recording migration %s: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: committing migration %s: %w", name, err)
	}

	return nil
}
