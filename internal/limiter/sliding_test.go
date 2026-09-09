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

func newTestSlidingWindowLimiter(t *testing.T, opts ...RedisOption) (*SlidingWindowLimiter, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return NewSlidingWindow(client, opts...), mr
}

func TestNewMemberID_Unique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id, err := newMemberID()
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
		if id == "" {
			t.Fatalf("call %d: got empty id", i+1)
		}
		if seen[id] {
			t.Fatalf("call %d: duplicate id %q", i+1, id)
		}
		seen[id] = true
	}
}

func TestSlidingWindowLimiter_LimitExhausted(t *testing.T) {
	l, _ := newTestSlidingWindowLimiter(t)
	ctx := context.Background()
	rule := Rule{Limit: 3, Window: time.Minute}

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
		t.Fatal("expected 4th request to be denied after limit exhausted")
	}
	if d.RetryAfter <= 0 {
		t.Fatal("expected RetryAfter to be set on denial")
	}
	if d.Remaining != 0 {
		t.Fatalf("expected Remaining 0 when denied, got %d", d.Remaining)
	}
}

func TestSlidingWindowLimiter_PerKeyIndependent(t *testing.T) {
	l, _ := newTestSlidingWindowLimiter(t)
	ctx := context.Background()
	rule := Rule{Limit: 1, Window: time.Minute}

	d1, err := l.Allow(ctx, "key-a", rule)
	if err != nil || !d1.Allowed {
		t.Fatalf("expected key-a first request allowed, got %+v err=%v", d1, err)
	}

	d2, err := l.Allow(ctx, "key-b", rule)
	if err != nil || !d2.Allowed {
		t.Fatalf("expected key-b first request allowed (independent window), got %+v err=%v", d2, err)
	}

	d3, err := l.Allow(ctx, "key-a", rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d3.Allowed {
		t.Fatal("expected key-a second request to be denied")
	}
}

func TestSlidingWindowLimiter_TTLSet(t *testing.T) {
	l, mr := newTestSlidingWindowLimiter(t)
	ctx := context.Background()
	rule := Rule{Limit: 5, Window: 10 * time.Second}

	if _, err := l.Allow(ctx, "ttl-key", rule); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	redisKey := slidingWindowKeyPrefix + "ttl-key"
	if !mr.Exists(redisKey) {
		t.Fatalf("expected key %q to exist", redisKey)
	}

	ttl := mr.TTL(redisKey)
	if ttl <= 0 {
		t.Fatalf("expected a positive TTL on %q, got %v", redisKey, ttl)
	}

	want := 40 * time.Second // window 10s + ttlBuffer 30s
	if diff := ttl - want; diff > 2*time.Second || diff < -2*time.Second {
		t.Fatalf("expected TTL close to %v, got %v", want, ttl)
	}
}

// This is the test that actually distinguishes the two algorithms: a token
// bucket refills continuously, so 5 seconds after emptying it would have
// ~5 tokens back and let a new request through. A sliding window log has
// no such thing — every one of the first 10 requests is still inside the
// trailing 10s window at +5s, so the 11th must be denied. Only once the
// oldest entries age out (at +10s) does capacity free up.
func TestSlidingWindowLimiter_StrictAcrossWindow(t *testing.T) {
	l, mr := newTestSlidingWindowLimiter(t)
	ctx := context.Background()
	rule := Rule{Limit: 10, Window: 10 * time.Second}

	start := time.Now()
	mr.SetTime(start)

	for i := 0; i < 10; i++ {
		d, err := l.Allow(ctx, "strict-key", rule)
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i+1, err)
		}
		if !d.Allowed {
			t.Fatalf("request %d: expected allowed, got denied", i+1)
		}
	}

	mr.SetTime(start.Add(5 * time.Second))
	d, err := l.Allow(ctx, "strict-key", rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Allowed {
		t.Fatal("expected request at +5s to be denied — the first 10 are still inside the 10s window")
	}

	mr.SetTime(start.Add(10 * time.Second))
	d, err = l.Allow(ctx, "strict-key", rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !d.Allowed {
		t.Fatal("expected request at +10s to be allowed — the first burst has aged out of the window")
	}
}

// Redis TIME has microsecond resolution, but the score we store is
// millisecond. Several requests landing in the same millisecond must not
// collide into a single sorted-set entry.
func TestSlidingWindowLimiter_UniqueMembersSameMillisecond(t *testing.T) {
	l, mr := newTestSlidingWindowLimiter(t)
	ctx := context.Background()
	rule := Rule{Limit: 10, Window: time.Minute}

	// Pin the clock: every Allow() below lands on the exact same instant.
	mr.SetTime(time.Now())

	for i := 0; i < 5; i++ {
		d, err := l.Allow(ctx, "unique-key", rule)
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i+1, err)
		}
		if !d.Allowed {
			t.Fatalf("request %d: expected allowed, got denied", i+1)
		}
	}

	members, err := mr.ZMembers(slidingWindowKeyPrefix + "unique-key")
	if err != nil {
		t.Fatalf("unexpected error reading zset: %v", err)
	}
	if len(members) != 5 {
		t.Fatalf("expected 5 distinct entries (one per request despite identical timestamp), got %d: %v", len(members), members)
	}
}

// 100 goroutines hammer the same key with a limit of 10. Scripts run
// atomically server-side, so exactly 10 must be allowed. See
// testCallTimeout in redis_test.go for why this needs a longer timeout
// than production's default.
func TestSlidingWindowLimiter_ConcurrentSameKey(t *testing.T) {
	l, _ := newTestSlidingWindowLimiter(t, WithCallTimeout(testCallTimeout))
	ctx := context.Background()
	rule := Rule{Limit: 10, Window: time.Minute}

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

func TestSlidingWindowLimiter_ConcurrentDifferentKeys(t *testing.T) {
	l, _ := newTestSlidingWindowLimiter(t, WithCallTimeout(testCallTimeout))
	ctx := context.Background()
	rule := Rule{Limit: 10, Window: time.Minute}

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
