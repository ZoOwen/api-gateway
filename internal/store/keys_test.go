package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestStore_CreateAndLookup(t *testing.T) {
	pool := requirePool(t)
	s := New(pool)
	ctx := context.Background()

	created, raw, err := s.Create(ctx, CreateParams{
		Name:        "create-lookup",
		Limit:       5,
		Window:      10 * time.Second,
		Burst:       5,
		Algo:        "token_bucket",
		UpstreamURL: "https://example.com",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected a non-empty id")
	}
	if created.KeyPrefix != raw[:8] {
		t.Fatalf("expected KeyPrefix %q to match the raw key's prefix, got %q", raw[:8], created.KeyPrefix)
	}
	if created.KeyHash == raw {
		t.Fatal("KeyHash must not equal the raw key — it should be hashed")
	}

	found, err := s.Lookup(ctx, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found.ID != created.ID {
		t.Fatalf("expected id %s, got %s", created.ID, found.ID)
	}
	if found.Limit != 5 || found.Window != 10*time.Second || found.Burst != 5 {
		t.Fatalf("unexpected rule fields: %+v", found)
	}
	if found.UpstreamURL != "https://example.com" {
		t.Fatalf("unexpected upstream url: %q", found.UpstreamURL)
	}
}

func TestStore_CreateRejectsInvalidAlgo(t *testing.T) {
	pool := requirePool(t)
	s := New(pool)

	_, _, err := s.Create(context.Background(), CreateParams{
		Name: "bad-algo", Limit: 1, Window: time.Minute, Algo: "not_a_real_algo", UpstreamURL: "https://example.com",
	})
	if err == nil {
		t.Fatal("expected an error for an invalid algo")
	}
}

func TestStore_LookupNotFound(t *testing.T) {
	pool := requirePool(t)
	s := New(pool)

	_, err := s.Lookup(context.Background(), "gw_this-key-does-not-exist")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestStore_RevokedKeyLooksNotFound(t *testing.T) {
	pool := requirePool(t)
	s := New(pool)
	ctx := context.Background()

	created, raw, err := s.Create(ctx, CreateParams{
		Name: "revoke-me", Limit: 1, Window: time.Minute, UpstreamURL: "https://example.com",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := s.Lookup(ctx, raw); err != nil {
		t.Fatalf("expected key to be found before revocation: %v", err)
	}

	if err := s.Revoke(ctx, created.ID); err != nil {
		t.Fatalf("unexpected error revoking: %v", err)
	}

	_, err = s.Lookup(ctx, raw)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound for a revoked key, got %v", err)
	}
}

func TestStore_RevokeUnknownID(t *testing.T) {
	pool := requirePool(t)
	s := New(pool)

	err := s.Revoke(context.Background(), "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestStore_List(t *testing.T) {
	pool := requirePool(t)
	s := New(pool)
	ctx := context.Background()

	// The table is a shared, persistent database that other tests (and
	// other test runs) also write to, so this asserts "the keys I just
	// created are all present" rather than an exact total row count.
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	wantNames := map[string]bool{
		"list-a-" + suffix: false,
		"list-b-" + suffix: false,
		"list-c-" + suffix: false,
	}
	for name := range wantNames {
		if _, _, err := s.Create(ctx, CreateParams{Name: name, Limit: 1, Window: time.Minute, UpstreamURL: "https://example.com"}); err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
	}

	keys, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := 0
	for _, k := range keys {
		if wantNames[k.Name] {
			t.Fatalf("saw name %q twice in the list", k.Name)
		}
		if _, ok := wantNames[k.Name]; ok {
			wantNames[k.Name] = true
			found++
		}
	}
	if found != 3 {
		t.Fatalf("expected to find all 3 created keys in the list, found %d (%v)", found, wantNames)
	}
}
