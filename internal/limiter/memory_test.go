package limiter

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock lets tests advance time deterministically instead of sleeping.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Now()}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestLimiter(clock *fakeClock) *MemoryLimiter {
	l := NewMemory()
	l.now = clock.Now
	return l
}

func TestMemoryLimiter_BurstExhausted(t *testing.T) {
	l := NewMemory()
	defer l.Stop()

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

func TestMemoryLimiter_RefillOverTime(t *testing.T) {
	clock := newFakeClock()
	l := newTestLimiter(clock)
	defer l.Stop()

	ctx := context.Background()
	rule := Rule{Limit: 1, Window: time.Second, Burst: 1}

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

	// Advance the fake clock instead of sleeping for real.
	clock.Advance(2 * time.Second)

	d, err = l.Allow(ctx, "refill-key", rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !d.Allowed {
		t.Fatal("expected request to be allowed after refill")
	}
}

func TestMemoryLimiter_PerKeyIndependent(t *testing.T) {
	l := NewMemory()
	defer l.Stop()

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

func TestMemoryLimiter_Cleanup(t *testing.T) {
	clock := newFakeClock()
	l := newTestLimiter(clock)
	defer l.Stop()

	ctx := context.Background()
	rule := Rule{Limit: 10, Window: time.Minute, Burst: 3}

	if _, err := l.Allow(ctx, "stale-key", rule); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	clock.Advance(idleTTL + time.Minute)
	l.cleanup()

	l.mu.Lock()
	_, exists := l.buckets["stale-key"]
	l.mu.Unlock()

	if exists {
		t.Fatal("expected stale bucket to be removed by cleanup")
	}
}

// 100 goroutines hammer the same key with a burst of 10. Exactly 10 must
// be allowed — more means a race let extra tokens through, fewer means a
// token was lost.
func TestMemoryLimiter_ConcurrentSameKey(t *testing.T) {
	l := NewMemory()
	defer l.Stop()

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

// 50 goroutines each hit a distinct key concurrently. All must be allowed,
// since each key has its own untouched bucket — this is about map access
// races, not bucket accounting.
func TestMemoryLimiter_ConcurrentDifferentKeys(t *testing.T) {
	l := NewMemory()
	defer l.Stop()

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

// Stop() must be safe to call while other goroutines are still calling
// Allow() — no panic, no data race.
func TestMemoryLimiter_StopWhileAllowing(t *testing.T) {
	l := NewMemory()

	ctx := context.Background()
	rule := Rule{Limit: 100, Window: time.Second, Burst: 100}

	const goroutines = 10
	const iterations = 1000

	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				_, _ = l.Allow(ctx, "key", rule)
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		l.Stop()
		l.Stop() // must be idempotent
	}()

	close(start)
	wg.Wait()
}
