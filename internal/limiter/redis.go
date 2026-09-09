package limiter

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed scripts/token_bucket.lua
var tokenBucketScript string

// defaultCallTimeout bounds a single Allow() call. A rate limiter that
// hangs is worse than one that gives up. Overridable via WithCallTimeout —
// production (main.go) leaves it at the default; tests hammering a
// single-threaded mock Redis under -race need more slack than a real
// Redis server ever would.
const defaultCallTimeout = 100 * time.Millisecond

// ttlBuffer is added on top of the time needed to refill a bucket from
// empty to full, so a key doesn't expire mid-burst.
const ttlBuffer = 30 * time.Second

const keyPrefix = "rl:tb:"

// RedisLimiter is a token bucket Limiter backed by Redis, so multiple
// gateway instances share the same buckets. The bucket math is identical
// to MemoryLimiter's lazy refill — see scripts/token_bucket.lua — the only
// difference is where "now" and the bucket state live.
type RedisLimiter struct {
	client      redis.Scripter
	script      *redis.Script
	callTimeout time.Duration
}

// callTimeoutSetter is implemented by every Redis-backed Limiter (token
// bucket, sliding window, ...) so WithCallTimeout can be shared across all
// of them instead of duplicated per algorithm.
type callTimeoutSetter interface {
	setCallTimeout(time.Duration)
}

func (l *RedisLimiter) setCallTimeout(d time.Duration) { l.callTimeout = d }

type RedisOption func(callTimeoutSetter)

// WithCallTimeout overrides the per-Allow() call timeout. Meant for tests;
// production should stick to the default.
func WithCallTimeout(d time.Duration) RedisOption {
	return func(l callTimeoutSetter) { l.setCallTimeout(d) }
}

func NewRedis(client redis.Scripter, opts ...RedisOption) *RedisLimiter {
	l := &RedisLimiter{
		client:      client,
		script:      redis.NewScript(tokenBucketScript),
		callTimeout: defaultCallTimeout,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

func (l *RedisLimiter) Allow(ctx context.Context, key string, rule Rule) (Decision, error) {
	capacity := rule.Burst
	if capacity <= 0 {
		capacity = rule.Limit
	}
	if capacity <= 0 {
		capacity = 1
	}

	var refillRate float64 // tokens per second
	if rule.Window > 0 {
		refillRate = float64(rule.Limit) / rule.Window.Seconds()
	}

	callCtx, cancel := context.WithTimeout(ctx, l.callTimeout)
	defer cancel()

	res, err := l.script.Run(callCtx, l.client, []string{keyPrefix + key},
		capacity, refillRate, 1, ttlSeconds(capacity, refillRate),
	).Result()
	if err != nil {
		return Decision{}, fmt.Errorf("limiter: redis token bucket: %w", err)
	}

	values, ok := res.([]interface{})
	if !ok || len(values) != 4 {
		return Decision{}, fmt.Errorf("limiter: unexpected redis script result: %#v", res)
	}

	allowed, err1 := toInt64(values[0])
	remaining, err2 := toInt64(values[1])
	retryAfterMs, err3 := toInt64(values[2])
	resetMs, err4 := toInt64(values[3])
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		return Decision{}, fmt.Errorf("limiter: parsing redis script result: %w", err)
	}

	decision := Decision{
		Allowed:   allowed == 1,
		Limit:     rule.Limit,
		Remaining: int(remaining),
		ResetAt:   time.Now().Add(time.Duration(resetMs) * time.Millisecond),
	}
	if !decision.Allowed {
		decision.RetryAfter = time.Duration(retryAfterMs) * time.Millisecond
	}

	return decision, nil
}

// ttlSeconds is how long an idle bucket key should live: the time to refill
// from empty to full, plus a fixed buffer, so single-shot callers don't
// leave keys around forever.
func ttlSeconds(capacity int, refillRate float64) int {
	if refillRate <= 0 {
		return int(ttlBuffer.Seconds())
	}
	fillTime := time.Duration(float64(capacity) / refillRate * float64(time.Second))
	return int((fillTime + ttlBuffer).Seconds())
}

// Close releases the underlying Redis client, if it owns one worth closing
// — redis.Scripter itself doesn't declare Close, only concrete clients
// like *redis.Client do.
func (l *RedisLimiter) Close() error {
	if closer, ok := l.client.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func toInt64(v interface{}) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("unexpected type %T", v)
	}
}
