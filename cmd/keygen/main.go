// Command keygen creates an API key from the terminal, for testing the
// gateway before a dashboard exists:
//
//	go run ./cmd/keygen -name "test" -limit 5 -window 10s -upstream https://...
//
// The raw key is printed exactly once, on creation. It is never stored —
// only its hash is — so if you lose it, the only fix is creating a new one.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/zoowen/gateway/internal/config"
	"github.com/zoowen/gateway/internal/store"
)

func main() {
	_ = godotenv.Load()

	name := flag.String("name", "", "human-readable name for the key (required)")
	limit := flag.Int("limit", 0, "requests allowed per window (required)")
	window := flag.Duration("window", 0, "window duration, e.g. 10s (required)")
	burst := flag.Int("burst", 0, "token bucket burst; ignored by sliding_window")
	algo := flag.String("algo", config.AlgoTokenBucket, "token_bucket | sliding_window")
	upstream := flag.String("upstream", "", "upstream base URL for this key (required)")
	databaseURL := flag.String("database-url", "", "defaults to $DATABASE_URL")
	flag.Parse()

	if *name == "" || *limit <= 0 || *window <= 0 || *upstream == "" {
		flag.Usage()
		log.Fatal("missing required flag: -name, -limit, -window, and -upstream are all required")
	}
	if *algo != config.AlgoTokenBucket && *algo != config.AlgoSlidingWindow {
		log.Fatalf("invalid -algo %q: must be one of: %s, %s", *algo, config.AlgoTokenBucket, config.AlgoSlidingWindow)
	}

	dsn := *databaseURL
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		log.Fatal("DATABASE_URL not set (env, .env, or -database-url)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("connecting to Postgres: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("pinging Postgres: %v", err)
	}

	if err := store.RunMigrations(ctx, pool); err != nil {
		log.Fatalf("running migrations: %v", err)
	}

	s := store.New(pool)
	key, raw, err := s.Create(ctx, store.CreateParams{
		Name:        *name,
		Limit:       *limit,
		Window:      *window,
		Burst:       *burst,
		Algo:        *algo,
		UpstreamURL: *upstream,
	})
	if err != nil {
		log.Fatalf("creating key: %v", err)
	}

	fmt.Printf("Created key %q (id=%s, limit=%d/%s, burst=%d, algo=%s, upstream=%s)\n",
		key.Name, key.ID, key.Limit, key.Window, key.Burst, key.Algo, key.UpstreamURL)
	fmt.Println()
	fmt.Println("Key (shown once — save it now, it cannot be retrieved later):")
	fmt.Println()
	fmt.Printf("  %s\n", raw)
	fmt.Println()
}
