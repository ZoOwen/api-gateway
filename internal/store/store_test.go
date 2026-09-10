package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

// testPool is nil when GATEWAY_TEST_DATABASE_URL isn't set — every
// Postgres-backed test calls requirePool(t) first, which skips instead of
// failing.
var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	// internal/store isn't the repo root, so .env (which lives at the
	// root, alongside go.mod) needs an explicit relative path here.
	_ = godotenv.Load("../../.env")

	dsn := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if dsn != "" {
		// A same-named env var from another project on this machine once
		// shadowed a more generically-named TEST_DATABASE_URL and pointed
		// our migrations at that project's database. This check doesn't
		// depend on the name staying unique forever, and it fails loud
		// (no test runs) rather than quietly running against whatever
		// database happened to be there.
		if err := RequireDatabaseName(dsn, "gateway"); err != nil {
			fmt.Fprintf(os.Stderr, "store: refusing to run tests: %v\n", err)
			os.Exit(1)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		pool, err := pgxpool.New(ctx, dsn)
		if err == nil {
			err = RunMigrations(ctx, pool)
		}
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "store: setting up GATEWAY_TEST_DATABASE_URL: %v\n", err)
			os.Exit(1)
		}
		testPool = pool
	}

	code := m.Run()
	if testPool != nil {
		testPool.Close()
	}
	os.Exit(code)
}

func requirePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testPool == nil {
		t.Skip("GATEWAY_TEST_DATABASE_URL not set, skipping Postgres-backed test")
	}
	return testPool
}
