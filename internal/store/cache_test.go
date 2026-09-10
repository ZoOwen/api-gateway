package store

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeLookuper struct {
	calls atomic.Int64
	fn    func(rawKey string) (*APIKey, error)
}

func (f *fakeLookuper) Lookup(ctx context.Context, rawKey string) (*APIKey, error) {
	f.calls.Add(1)
	return f.fn(rawKey)
}

func TestCache_Hit(t *testing.T) {
	want := &APIKey{ID: "k1", Active: true}
	source := &fakeLookuper{fn: func(string) (*APIKey, error) { return want, nil }}
	c := NewCache(source, 30*time.Second)
	ctx := context.Background()

	got1, err := c.Lookup(ctx, "raw")
	if err != nil || got1 != want {
		t.Fatalf("first lookup: got %+v err=%v", got1, err)
	}

	got2, err := c.Lookup(ctx, "raw")
	if err != nil || got2 != want {
		t.Fatalf("second lookup: got %+v err=%v", got2, err)
	}

	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("expected source queried once (second lookup should hit cache), got %d calls", calls)
	}
}

func TestCache_Miss(t *testing.T) {
	source := &fakeLookuper{fn: func(string) (*APIKey, error) { return nil, ErrKeyNotFound }}
	c := NewCache(source, 30*time.Second)

	_, err := c.Lookup(context.Background(), "raw")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestCache_NegativeCaching(t *testing.T) {
	source := &fakeLookuper{fn: func(string) (*APIKey, error) { return nil, ErrKeyNotFound }}
	c := NewCache(source, 30*time.Second)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, err := c.Lookup(ctx, "raw")
		if !errors.Is(err, ErrKeyNotFound) {
			t.Fatalf("call %d: expected ErrKeyNotFound, got %v", i+1, err)
		}
	}

	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("expected source queried once (misses after the first should be cached), got %d calls", calls)
	}
}

func TestCache_TTLExpires(t *testing.T) {
	source := &fakeLookuper{fn: func(string) (*APIKey, error) { return &APIKey{ID: "k1", Active: true}, nil }}
	c := NewCache(source, 30*time.Second)
	ctx := context.Background()

	fixed := time.Now()
	c.now = func() time.Time { return fixed }

	if _, err := c.Lookup(ctx, "raw"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := c.Lookup(ctx, "raw"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("expected 1 call before TTL expiry, got %d", calls)
	}

	c.now = func() time.Time { return fixed.Add(31 * time.Second) }
	if _, err := c.Lookup(ctx, "raw"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls := source.calls.Load(); calls != 2 {
		t.Fatalf("expected 2 calls after TTL expiry, got %d", calls)
	}
}

// This is the explicitly-required trade-off test: a key revoked in the
// source keeps being served by the cache until the cached entry's TTL
// runs out. That's documented behavior (see Cache's doc comment), not a
// bug — asserting it here is what keeps it that way on purpose.
func TestCache_RevokedKeyServedUntilTTLExpires(t *testing.T) {
	revoked := false
	source := &fakeLookuper{fn: func(string) (*APIKey, error) {
		if revoked {
			// Store.Lookup filters WHERE active, so a revoked key looks
			// identical to a nonexistent one by the time it reaches here.
			return nil, ErrKeyNotFound
		}
		return &APIKey{ID: "k1", Active: true}, nil
	}}
	c := NewCache(source, 30*time.Second)
	ctx := context.Background()

	fixed := time.Now()
	c.now = func() time.Time { return fixed }

	key, err := c.Lookup(ctx, "raw")
	if err != nil || key == nil {
		t.Fatalf("expected key to be found before revocation, got %+v err=%v", key, err)
	}

	revoked = true

	key, err = c.Lookup(ctx, "raw")
	if err != nil || key == nil {
		t.Fatalf("expected revoked key to still be served within TTL (documented trade-off), got %+v err=%v", key, err)
	}

	c.now = func() time.Time { return fixed.Add(31 * time.Second) }
	_, err = c.Lookup(ctx, "raw")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound once the cache re-queries past TTL, got %v", err)
	}
}

func TestCache_SourceErrorNotCached(t *testing.T) {
	wantErr := errors.New("boom")
	source := &fakeLookuper{fn: func(string) (*APIKey, error) { return nil, wantErr }}
	c := NewCache(source, 30*time.Second)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		_, err := c.Lookup(ctx, "raw")
		if !errors.Is(err, wantErr) {
			t.Fatalf("call %d: expected source error, got %v", i+1, err)
		}
	}

	if calls := source.calls.Load(); calls != 2 {
		t.Fatalf("expected source queried every time on non-ErrKeyNotFound errors, got %d calls", calls)
	}
}
