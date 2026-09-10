package store

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// RequireDatabaseName parses dsn and returns an error unless its database
// name contains want. Meant to be called before running migrations against
// a test DSN pulled from the environment: a same-named env var from an
// unrelated project can silently shadow this project's own (see
// GATEWAY_TEST_DATABASE_URL's naming — this happened once, with a bare
// TEST_DATABASE_URL, and applied our migrations to that other project's
// database). That was harmless because every migration so far is additive.
// It will not stay harmless once a migration drops or alters something —
// at that point the wrong database means real data loss, not just an
// unwelcome extra table.
func RequireDatabaseName(dsn, want string) error {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("store: parsing DSN: %w", err)
	}
	if !strings.Contains(cfg.Database, want) {
		return fmt.Errorf("store: refusing to run against database %q (expected its name to contain %q)", cfg.Database, want)
	}
	return nil
}
