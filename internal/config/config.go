package config

import (
	"fmt"
	"net/url"
	"os"

	"github.com/joho/godotenv"
)

const (
	AlgoTokenBucket   = "token_bucket"
	AlgoSlidingWindow = "sliding_window"
)

type Config struct {
	Port        string
	UpstreamURL *url.URL // nil when DatabaseURL is set — upstream comes from each API key instead
	RedisURL    string
	LimiterAlgo string
	DatabaseURL string
	AdminToken  string // protects /admin/* and /debug/pprof/*; empty means those stay locked
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	databaseURL := os.Getenv("DATABASE_URL")

	// UPSTREAM_URL is only required in the legacy single-upstream mode
	// (no DATABASE_URL): with a database configured, each API key carries
	// its own upstream_url instead.
	var upstreamURL *url.URL
	if rawUpstream := os.Getenv("UPSTREAM_URL"); rawUpstream != "" {
		u, err := url.Parse(rawUpstream)
		if err != nil {
			return nil, fmt.Errorf("UPSTREAM_URL is not a valid URL: %w", err)
		}
		if u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("UPSTREAM_URL is not a valid absolute URL: %q", rawUpstream)
		}
		upstreamURL = u
	}
	if databaseURL == "" && upstreamURL == nil {
		return nil, fmt.Errorf("UPSTREAM_URL is not set (required when DATABASE_URL is empty)")
	}

	algo := os.Getenv("LIMITER_ALGO")
	if algo == "" {
		algo = AlgoTokenBucket
	}
	if algo != AlgoTokenBucket && algo != AlgoSlidingWindow {
		return nil, fmt.Errorf("invalid LIMITER_ALGO %q: must be one of: %s, %s", algo, AlgoTokenBucket, AlgoSlidingWindow)
	}

	return &Config{
		Port:        port,
		UpstreamURL: upstreamURL,
		RedisURL:    os.Getenv("REDIS_URL"),
		LimiterAlgo: algo,
		DatabaseURL: databaseURL,
		AdminToken:  os.Getenv("ADMIN_TOKEN"),
	}, nil
}
