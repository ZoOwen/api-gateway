package limiter

import (
	"context"
	"sync"
	"time"
)

const idleTTL = 10 * time.Minute

type bucket struct {
	tokens     float64
	lastRefill time.Time
	lastAccess time.Time
}

// MemoryLimiter is an in-memory token bucket limiter, one bucket per key.
// Refill is lazy (computed from elapsed time on each Allow call) rather than
// driven by a ticker, so the same logic ports directly to a Redis-backed
// implementation later.
type MemoryLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	now func() time.Time // overridable in tests

	stopCh   chan struct{}
	stopOnce sync.Once
}

func NewMemory() *MemoryLimiter {
	l := &MemoryLimiter{
		buckets: make(map[string]*bucket),
		now:     time.Now,
		stopCh:  make(chan struct{}),
	}
	go l.cleanupLoop()
	return l
}

func (l *MemoryLimiter) Allow(ctx context.Context, key string, rule Rule) (Decision, error) {
	capacity := float64(rule.Burst)
	if rule.Burst <= 0 {
		capacity = float64(rule.Limit)
	}
	if capacity <= 0 {
		capacity = 1
	}

	var refillRate float64 // tokens per second
	if rule.Window > 0 {
		refillRate = float64(rule.Limit) / rule.Window.Seconds()
	}

	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: capacity, lastRefill: now}
		l.buckets[key] = b
	}

	if elapsed := now.Sub(b.lastRefill).Seconds(); elapsed > 0 {
		b.tokens += elapsed * refillRate
		if b.tokens > capacity {
			b.tokens = capacity
		}
		b.lastRefill = now
	}
	b.lastAccess = now

	decision := Decision{Limit: rule.Limit}

	if b.tokens >= 1 {
		b.tokens--
		decision.Allowed = true
	} else if refillRate > 0 {
		missing := 1 - b.tokens
		decision.RetryAfter = time.Duration(missing / refillRate * float64(time.Second))
	}

	decision.Remaining = int(b.tokens)

	if refillRate > 0 {
		missingToFull := capacity - b.tokens
		if missingToFull < 0 {
			missingToFull = 0
		}
		decision.ResetAt = now.Add(time.Duration(missingToFull / refillRate * float64(time.Second)))
	} else {
		decision.ResetAt = now
	}

	return decision, nil
}

func (l *MemoryLimiter) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			l.cleanup()
		case <-l.stopCh:
			return
		}
	}
}

func (l *MemoryLimiter) cleanup() {
	cutoff := l.now().Add(-idleTTL)

	l.mu.Lock()
	defer l.mu.Unlock()

	for key, b := range l.buckets {
		if b.lastAccess.Before(cutoff) {
			delete(l.buckets, key)
		}
	}
}

// Stop terminates the background cleanup goroutine. Safe to call more than once.
func (l *MemoryLimiter) Stop() {
	l.stopOnce.Do(func() {
		close(l.stopCh)
	})
}

// Close implements Limiter's Close by stopping the cleanup goroutine.
func (l *MemoryLimiter) Close() error {
	l.Stop()
	return nil
}
