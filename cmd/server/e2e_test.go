package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"

	"github.com/zoowen/gateway/internal/auth"
	"github.com/zoowen/gateway/internal/limiter"
	"github.com/zoowen/gateway/internal/proxy"
	"github.com/zoowen/gateway/internal/store"
)

// TestEndToEnd_TwoKeysHaveSeparateLimits wires auth -> rate limit -> proxy
// the same way main's newProxyHandler does (duplicated here rather than
// extracted, since it's two lines) and proves two API keys with different
// limits don't share a rate-limit bucket: exhausting key A must not affect
// key B's budget.
func TestEndToEnd_TwoKeysHaveSeparateLimits(t *testing.T) {
	_ = godotenv.Load("../../.env")

	dsn := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("GATEWAY_TEST_DATABASE_URL not set, skipping end-to-end test")
	}
	if err := store.RequireDatabaseName(dsn, "gateway"); err != nil {
		t.Fatalf("refusing to run: %v", err)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer pool.Close()

	if err := store.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("running migrations: %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	s := store.New(pool)

	_, rawA, err := s.Create(ctx, store.CreateParams{
		Name: "e2e-a", Limit: 2, Window: time.Minute, Burst: 2, UpstreamURL: upstream.URL,
	})
	if err != nil {
		t.Fatalf("creating key A: %v", err)
	}
	_, rawB, err := s.Create(ctx, store.CreateParams{
		Name: "e2e-b", Limit: 5, Window: time.Minute, Burst: 5, UpstreamURL: upstream.URL,
	})
	if err != nil {
		t.Fatalf("creating key B: %v", err)
	}

	rl := limiter.NewMemory()
	defer func() { _ = rl.Close() }()

	upstreamFn := func(r *http.Request) (*url.URL, error) {
		key, _ := auth.FromContext(r.Context())
		return url.Parse(key.UpstreamURL)
	}

	handler := auth.Middleware(s)(limiter.Middleware(rl, ruleFromContext, keyIDFromContext)(proxy.New(upstreamFn)))

	srv := httptest.NewServer(handler)
	defer srv.Close()

	doRequest := func(raw string) int {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/proxy/ping", nil)
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+raw)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request error: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	for i := 1; i <= 2; i++ {
		if code := doRequest(rawA); code != http.StatusOK {
			t.Fatalf("key A request %d: expected 200, got %d", i, code)
		}
	}
	if code := doRequest(rawA); code != http.StatusTooManyRequests {
		t.Fatalf("key A request 3: expected 429 (limit is 2), got %d", code)
	}

	// Key B has its own separate limit of 5 and hasn't been touched yet —
	// it must not be affected by key A being exhausted.
	for i := 1; i <= 5; i++ {
		if code := doRequest(rawB); code != http.StatusOK {
			t.Fatalf("key B request %d: expected 200, got %d", i, code)
		}
	}
	if code := doRequest(rawB); code != http.StatusTooManyRequests {
		t.Fatalf("key B request 6: expected 429 (limit is 5), got %d", code)
	}
}

// TestEndToEnd_PerKeyAlgoDispatch is the actual target of the algo-per-key
// fix: a token_bucket key and a sliding_window key, hitting the same
// running server at the same time, must get genuinely different
// rate-limiting behavior — not just different config values that happen to
// go unused. Same distinguishing pattern as the lower-level
// TestRegistry_AlgoPerKeyBehaviorDiffers (10 requests, +5s: token bucket
// has refilled and allows, sliding window hasn't aged anything out and
// denies), but exercised through the full auth -> rate limit -> proxy
// chain with real Postgres-backed keys instead of calling Registry
// directly.
func TestEndToEnd_PerKeyAlgoDispatch(t *testing.T) {
	_ = godotenv.Load("../../.env")

	dsn := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("GATEWAY_TEST_DATABASE_URL not set, skipping end-to-end test")
	}
	if err := store.RequireDatabaseName(dsn, "gateway"); err != nil {
		t.Fatalf("refusing to run: %v", err)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer pool.Close()

	if err := store.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("running migrations: %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	s := store.New(pool)

	_, rawTB, err := s.Create(ctx, store.CreateParams{
		Name: "e2e-algo-tb", Limit: 10, Window: 10 * time.Second, Burst: 10, Algo: "token_bucket", UpstreamURL: upstream.URL,
	})
	if err != nil {
		t.Fatalf("creating token_bucket key: %v", err)
	}
	_, rawSW, err := s.Create(ctx, store.CreateParams{
		Name: "e2e-algo-sw", Limit: 10, Window: 10 * time.Second, Algo: "sliding_window", UpstreamURL: upstream.URL,
	})
	if err != nil {
		t.Fatalf("creating sliding_window key: %v", err)
	}

	// miniredis, not a real Redis, so this test stays self-contained like
	// the rest of internal/limiter's Redis-backed tests — only the two
	// API key rows need real Postgres.
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = redisClient.Close() }()

	rl := limiter.NewRegistry(map[string]limiter.Limiter{
		"token_bucket":   limiter.NewRedis(redisClient),
		"sliding_window": limiter.NewSlidingWindow(redisClient),
	}, "token_bucket")
	defer func() { _ = rl.Close() }()

	handler := auth.Middleware(s)(limiter.Middleware(rl, ruleFromContext, keyIDFromContext)(proxy.New(upstreamFromContext)))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	doAlgoRequest := func(raw string) int {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/proxy/ping", nil)
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+raw)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request error: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	start := time.Now()
	mr.SetTime(start)

	for i := 1; i <= 10; i++ {
		if code := doAlgoRequest(rawTB); code != http.StatusOK {
			t.Fatalf("token_bucket request %d: expected 200, got %d", i, code)
		}
		if code := doAlgoRequest(rawSW); code != http.StatusOK {
			t.Fatalf("sliding_window request %d: expected 200, got %d", i, code)
		}
	}

	mr.SetTime(start.Add(5 * time.Second))

	if code := doAlgoRequest(rawTB); code != http.StatusOK {
		t.Fatalf("expected token_bucket to allow at +5s (refilled ~5 tokens), got %d", code)
	}
	if code := doAlgoRequest(rawSW); code != http.StatusTooManyRequests {
		t.Fatalf("expected sliding_window to deny at +5s (still inside the 10s window), got %d", code)
	}
}
