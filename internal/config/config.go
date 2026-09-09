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
	UpstreamURL *url.URL
	RedisURL    string
	LimiterAlgo string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	rawUpstream := os.Getenv("UPSTREAM_URL")
	if rawUpstream == "" {
		return nil, fmt.Errorf("UPSTREAM_URL is not set")
	}

	upstreamURL, err := url.Parse(rawUpstream)
	if err != nil {
		return nil, fmt.Errorf("UPSTREAM_URL is not a valid URL: %w", err)
	}
	if upstreamURL.Scheme == "" || upstreamURL.Host == "" {
		return nil, fmt.Errorf("UPSTREAM_URL is not a valid absolute URL: %q", rawUpstream)
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
	}, nil
}
