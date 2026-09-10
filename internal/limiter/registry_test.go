package limiter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type fakeLimiter struct {
	calls    int
	closed   bool
	closeErr error
}

func (f *fakeLimiter) Allow(ctx context.Context, key string, rule Rule) (Decision, error) {
	f.calls++
	return Decision{Allowed: true, Limit: rule.Limit}, nil
}

func (f *fakeLimiter) Close() error {
	f.closed = true
	return f.closeErr
}

func TestRegistry_DispatchesByAlgo(t *testing.T) {
	tb := &fakeLimiter{}
	sw := &fakeLimiter{}
	reg := NewRegistry(map[string]Limiter{"token_bucket": tb, "sliding_window": sw}, "token_bucket")
	ctx := context.Background()

	if _, err := reg.Allow(ctx, "k", Rule{Algo: "token_bucket"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := reg.Allow(ctx, "k", Rule{Algo: "sliding_window"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tb.calls != 1 {
		t.Fatalf("expected token_bucket limiter called once, got %d", tb.calls)
	}
	if sw.calls != 1 {
		t.Fatalf("expected sliding_window limiter called once, got %d", sw.calls)
	}
}

func TestRegistry_EmptyAlgoUsesDefault(t *testing.T) {
	tb := &fakeLimiter{}
	sw := &fakeLimiter{}
	reg := NewRegistry(map[string]Limiter{"token_bucket": tb, "sliding_window": sw}, "token_bucket")

	if _, err := reg.Allow(context.Background(), "k", Rule{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tb.calls != 1 {
		t.Fatalf("expected the default (token_bucket) limiter called, got %d calls", tb.calls)
	}
	if sw.calls != 0 {
		t.Fatalf("expected sliding_window untouched, got %d calls", sw.calls)
	}
}

func TestRegistry_UnknownAlgoReturnsError(t *testing.T) {
	reg := NewRegistry(map[string]Limiter{"token_bucket": &fakeLimiter{}}, "token_bucket")

	_, err := reg.Allow(context.Background(), "k", Rule{Algo: "bogus"})
	if err == nil {
		t.Fatal("expected an error for an unknown algo")
	}
}

func TestRegistry_Close(t *testing.T) {
	tb := &fakeLimiter{}
	sw := &fakeLimiter{}
	reg := NewRegistry(map[string]Limiter{"token_bucket": tb, "sliding_window": sw}, "token_bucket")

	if err := reg.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tb.closed || !sw.closed {
		t.Fatalf("expected both limiters closed, got tb=%v sw=%v", tb.closed, sw.closed)
	}
}

func TestRegistry_CloseAggregatesErrors(t *testing.T) {
	err1 := errors.New("err1")
	err2 := errors.New("err2")
	reg := NewRegistry(map[string]Limiter{
		"a": &fakeLimiter{closeErr: err1},
		"b": &fakeLimiter{closeErr: err2},
	}, "a")

	err := reg.Close()
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, err1) || !errors.Is(err, err2) {
		t.Fatalf("expected the combined error to wrap both, got %v", err)
	}
}

// This is the actual point of Registry: the same key, hitting the same
// running Registry, gets genuinely different rate-limiting behavior
// depending on Rule.Algo — not just a different return value from a fake.
// Same pattern as TestSlidingWindowLimiter_StrictAcrossWindow: 10 requests
// land instantly, then +5s. A token bucket has refilled ~5 tokens by then
// and allows; a sliding window still has all 10 entries inside its window
// and denies. Both algorithms run against the same Redis, dispatched by
// the same Registry, proving the two don't interfere with each other.
func TestRegistry_AlgoPerKeyBehaviorDiffers(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	reg := NewRegistry(map[string]Limiter{
		"token_bucket":   NewRedis(client),
		"sliding_window": NewSlidingWindow(client),
	}, "token_bucket")
	t.Cleanup(func() { _ = reg.Close() })

	ctx := context.Background()
	ruleFor := func(algo string) Rule {
		return Rule{Limit: 10, Window: 10 * time.Second, Burst: 10, Algo: algo}
	}

	start := time.Now()
	mr.SetTime(start)

	for i := 0; i < 10; i++ {
		for _, algo := range []string{"token_bucket", "sliding_window"} {
			d, err := reg.Allow(ctx, "shared-key", ruleFor(algo))
			if err != nil {
				t.Fatalf("%s request %d: unexpected error: %v", algo, i+1, err)
			}
			if !d.Allowed {
				t.Fatalf("%s request %d: expected allowed, got denied", algo, i+1)
			}
		}
	}

	mr.SetTime(start.Add(5 * time.Second))

	tbDecision, err := reg.Allow(ctx, "shared-key", ruleFor("token_bucket"))
	if err != nil {
		t.Fatalf("token_bucket: unexpected error: %v", err)
	}
	if !tbDecision.Allowed {
		t.Fatal("expected token_bucket to allow at +5s — it should have refilled ~5 tokens")
	}

	swDecision, err := reg.Allow(ctx, "shared-key", ruleFor("sliding_window"))
	if err != nil {
		t.Fatalf("sliding_window: unexpected error: %v", err)
	}
	if swDecision.Allowed {
		t.Fatal("expected sliding_window to deny at +5s — the first 10 are still inside the 10s window")
	}
}
