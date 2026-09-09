package limiter

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedisLimiter(t *testing.T, opts ...RedisOption) (*RedisLimiter, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return NewRedis(client, opts...), mr
}

func TestRedisLimiter_BurstExhausted(t *testing.T) {
	l, _ := newTestRedisLimiter(t)
	ctx := context.Background()
	rule := Rule{Limit: 10, Window: time.Minute, Burst: 3}

	for i := 0; i < 3; i++ {
		d, err := l.Allow(ctx, "test-key", rule)
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i+1, err)
		}
		if !d.Allowed {
			t.Fatalf("request %d: expected allowed, got denied", i+1)
		}
	}

	d, err := l.Allow(ctx, "test-key", rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Allowed {
		t.Fatal("expected 4th request to be denied after burst exhausted")
	}
	if d.RetryAfter <= 0 {
		t.Fatal("expected RetryAfter to be set on denial")
	}
	if d.Remaining != 0 {
		t.Fatalf("expected Remaining 0 when denied, got %d", d.Remaining)
	}
}

func TestRedisLimiter_RefillOverTime(t *testing.T) {
	l, mr := newTestRedisLimiter(t)
	ctx := context.Background()
	rule := Rule{Limit: 1, Window: time.Second, Burst: 1}

	// Pin miniredis's clock — TIME inside the Lua script reads from this,
	// not FastForward (which only decrements key TTLs, not TIME's clock).
	start := time.Now()
	mr.SetTime(start)

	d, err := l.Allow(ctx, "refill-key", rule)
	if err != nil || !d.Allowed {
		t.Fatalf("expected first request allowed, got %+v err=%v", d, err)
	}

	d, err = l.Allow(ctx, "refill-key", rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Allowed {
		t.Fatal("expected second immediate request to be denied (bucket empty)")
	}

	mr.SetTime(start.Add(2 * time.Second))

	d, err = l.Allow(ctx, "refill-key", rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !d.Allowed {
		t.Fatal("expected request to be allowed after refill")
	}
}

func TestRedisLimiter_PerKeyIndependent(t *testing.T) {
	l, _ := newTestRedisLimiter(t)
	ctx := context.Background()
	rule := Rule{Limit: 1, Window: time.Minute, Burst: 1}

	d1, err := l.Allow(ctx, "key-a", rule)
	if err != nil || !d1.Allowed {
		t.Fatalf("expected key-a first request allowed, got %+v err=%v", d1, err)
	}

	d2, err := l.Allow(ctx, "key-b", rule)
	if err != nil || !d2.Allowed {
		t.Fatalf("expected key-b first request allowed (independent bucket), got %+v err=%v", d2, err)
	}

	d3, err := l.Allow(ctx, "key-a", rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d3.Allowed {
		t.Fatal("expected key-a second request to be denied")
	}
}

func TestRedisLimiter_TTLSet(t *testing.T) {
	l, mr := newTestRedisLimiter(t)
	ctx := context.Background()
	// capacity 10, rate 1 token/sec -> fill time 10s, +30s buffer = 40s.
	rule := Rule{Limit: 1, Window: time.Second, Burst: 10}

	if _, err := l.Allow(ctx, "ttl-key", rule); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	redisKey := keyPrefix + "ttl-key"
	if !mr.Exists(redisKey) {
		t.Fatalf("expected key %q to exist", redisKey)
	}

	ttl := mr.TTL(redisKey)
	if ttl <= 0 {
		t.Fatalf("expected a positive TTL on %q, got %v", redisKey, ttl)
	}

	want := 40 * time.Second
	if diff := ttl - want; diff > 2*time.Second || diff < -2*time.Second {
		t.Fatalf("expected TTL close to %v, got %v", want, ttl)
	}
}

// testCallTimeout gives the contention tests below more slack than
// production's default 100ms. miniredis is a single-mutex, non-JIT Lua
// interpreter — nothing like real Redis — and under -race, 100 fully
// concurrent script calls against it can individually run past 100ms on a
// busy machine even though nothing is actually stuck. That's a property of
// the test double, not of RedisLimiter; the timeout itself is exercised by
// TestRedisLimiter_BurstExhausted and friends at the default.
const testCallTimeout = 2 * time.Second

// 100 goroutines hammer the same key through Redis with a burst of 10.
// Scripts run atomically server-side, so exactly 10 must be allowed.
func TestRedisLimiter_ConcurrentSameKey(t *testing.T) {
	l, _ := newTestRedisLimiter(t, WithCallTimeout(testCallTimeout))
	ctx := context.Background()
	rule := Rule{Limit: 10, Window: time.Minute, Burst: 10}

	const goroutines = 100
	var wg sync.WaitGroup
	var allowed atomic.Int64

	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			d, err := l.Allow(ctx, "shared-key", rule)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if d.Allowed {
				allowed.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := allowed.Load(); got != 10 {
		t.Fatalf("expected exactly 10 allowed requests, got %d", got)
	}
}

func TestRedisLimiter_ConcurrentDifferentKeys(t *testing.T) {
	l, _ := newTestRedisLimiter(t, WithCallTimeout(testCallTimeout))
	ctx := context.Background()
	rule := Rule{Limit: 10, Window: time.Minute, Burst: 10}

	const goroutines = 50
	var wg sync.WaitGroup
	var allowed atomic.Int64

	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		key := fmt.Sprintf("key-%d", i)
		go func(key string) {
			defer wg.Done()
			<-start
			d, err := l.Allow(ctx, key, rule)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if d.Allowed {
				allowed.Add(1)
			}
		}(key)
	}
	close(start)
	wg.Wait()

	if got := allowed.Load(); got != goroutines {
		t.Fatalf("expected all %d requests allowed (distinct keys), got %d", goroutines, got)
	}
}
