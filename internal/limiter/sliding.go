package limiter

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed scripts/sliding_window.lua
var slidingWindowScript string

const slidingWindowKeyPrefix = "rl:sw:"

// SlidingWindowLimiter is a sliding-window-log Limiter backed by Redis: it
// keeps one timestamped sorted-set entry per allowed request and counts how
// many fall inside the trailing window, so the average rate is capped
// exactly — unlike the token bucket, it has no burst allowance. A caller
// idle for a minute cannot fire 50 requests at once just because a bucket
// refilled; it's still bound by Limit-per-Window at any instant.
//
// Rule.Burst is ignored by this algorithm — only Limit and Window apply.
// The cost of the stricter guarantee is memory: one sorted-set entry per
// allowed request in the window, versus the token bucket's two numbers
// per key.
type SlidingWindowLimiter struct {
	client      redis.Scripter
	script      *redis.Script
	callTimeout time.Duration
}

func (l *SlidingWindowLimiter) setCallTimeout(d time.Duration) { l.callTimeout = d }

func NewSlidingWindow(client redis.Scripter, opts ...RedisOption) *SlidingWindowLimiter {
	l := &SlidingWindowLimiter{
		client:      client,
		script:      redis.NewScript(slidingWindowScript),
		callTimeout: defaultCallTimeout,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

func (l *SlidingWindowLimiter) Allow(ctx context.Context, key string, rule Rule) (Decision, error) {
	windowMs := rule.Window.Milliseconds()
	ttlSeconds := int((rule.Window + ttlBuffer).Seconds())

	memberID, err := newMemberID()
	if err != nil {
		return Decision{}, fmt.Errorf("limiter: generating member id: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, l.callTimeout)
	defer cancel()

	res, err := l.script.Run(callCtx, l.client, []string{slidingWindowKeyPrefix + key},
		rule.Limit, windowMs, ttlSeconds, memberID,
	).Result()
	if err != nil {
		return Decision{}, fmt.Errorf("limiter: redis sliding window: %w", err)
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

// newMemberID returns a short random identifier for a sorted-set member.
// It only needs to not collide with another member of the same key — see
// the rationale in scripts/sliding_window.lua for why this comes from Go
// rather than from anything Redis-side.
func newMemberID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (l *SlidingWindowLimiter) Close() error {
	if closer, ok := l.client.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
