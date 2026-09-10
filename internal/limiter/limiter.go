package limiter

import (
	"context"
	"time"
)

// Rule is passed per-call rather than baked into the constructor because
// each API key carries its own limit loaded from Postgres.
type Rule struct {
	Limit  int           // berapa request
	Window time.Duration // per berapa lama
	Burst  int           // kapasitas ember, token bucket only
	// Algo selects which algorithm handles this call — only Registry reads
	// it, to pick which underlying Limiter to delegate to. Every concrete
	// Limiter (MemoryLimiter, RedisLimiter, SlidingWindowLimiter) ignores
	// it: each one already knows what it is, it never needs telling.
	// Empty means "use the registry's default".
	Algo string
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
