package limiter

import (
	"context"
	"time"
)

// Rule is passed per-call rather than baked into the constructor because
// each API key will eventually carry its own limit loaded from Postgres.
type Rule struct {
	Limit  int           // berapa request
	Window time.Duration // per berapa lama
	Burst  int           // kapasitas ember, token bucket only
}

type Decision struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration // diisi cuma kalau ditolak
	ResetAt    time.Time
}

type Limiter interface {
	Allow(ctx context.Context, key string, rule Rule) (Decision, error)
	Close() error
}
