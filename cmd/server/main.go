package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"net/http/pprof"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/zoowen/gateway/internal/auth"
	"github.com/zoowen/gateway/internal/config"
	"github.com/zoowen/gateway/internal/limiter"
	"github.com/zoowen/gateway/internal/metrics"
	"github.com/zoowen/gateway/internal/proxy"
	"github.com/zoowen/gateway/internal/store"
)

const cacheTTL = 30 * time.Second

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := newResponseWriter(w)

		next.ServeHTTP(rw, r)

		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rw.statusCode, time.Since(start))
	})
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// clientKey is the legacy (no-database) rate-limit key: one bucket per IP.
func clientKey(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return fwd
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// newLimiter builds a Registry over whatever algorithms are actually
// available given the current config, so a single running server can serve
// both token_bucket and sliding_window keys at once (see Rule.Algo and
// Registry) — LIMITER_ALGO only picks the *default* used when a key (or
// the legacy static Rule) doesn't specify one.
//
// Without REDIS_URL, sliding_window has no in-memory equivalent, so the
// registry only ever contains token_bucket. That's still a fatal error at
// startup if LIMITER_ALGO itself asks for sliding_window (the legacy mode
// would have nothing to fall back to) — but if DATABASE_URL is set and
// some individual key's algo is sliding_window, that only fails that key's
// requests at request time (Registry.Allow returns an error, which
// Middleware's fail-open handles), not the whole server.
func newLimiter(cfg *config.Config) limiter.Limiter {
	limiters := make(map[string]limiter.Limiter)

	if cfg.RedisURL == "" {
		if cfg.LimiterAlgo == config.AlgoSlidingWindow {
			log.Fatalf("LIMITER_ALGO=%s requires REDIS_URL (no in-memory implementation)", config.AlgoSlidingWindow)
		}
		log.Println("rate limiter: REDIS_URL not set, using in-memory token bucket only")
		limiters[config.AlgoTokenBucket] = limiter.NewMemory()
		return limiter.NewRegistry(limiters, cfg.LimiterAlgo)
	}

	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatalf("invalid REDIS_URL: %v", err)
	}

	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		log.Fatalf("failed to connect to Redis at %s: %v", opts.Addr, err)
	}

	log.Printf("rate limiter: using Redis at %s (token_bucket + sliding_window, default=%s)", opts.Addr, cfg.LimiterAlgo)
	limiters[config.AlgoTokenBucket] = limiter.NewRedis(client)
	limiters[config.AlgoSlidingWindow] = limiter.NewSlidingWindow(client)
	return limiter.NewRegistry(limiters, cfg.LimiterAlgo)
}

// upstreamFromContext reads the target upstream from the *store.APIKey
// that auth.Middleware put in the request's context. It's only ever
// called after auth has run (see main's middleware order), so a missing
// key means a wiring bug, not a client error.
func upstreamFromContext(r *http.Request) (*url.URL, error) {
	key, ok := auth.FromContext(r.Context())
	if !ok {
		return nil, errors.New("no api key in request context")
	}
	u, err := url.Parse(key.UpstreamURL)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// ruleFromContext builds a rate-limit Rule from the request's *store.APIKey
// instead of a hardcoded value — each key carries its own limit/window/
// burst/algo from Postgres.
func ruleFromContext(r *http.Request) limiter.Rule {
	key, ok := auth.FromContext(r.Context())
	if !ok {
		return limiter.Rule{}
	}
	return limiter.Rule{Limit: key.Limit, Window: key.Window, Burst: key.Burst, Algo: key.Algo}
}

// keyIDFromContext is the per-API-key rate-limit key — each key gets its
// own bucket/window, keyed by id rather than by client IP.
func keyIDFromContext(r *http.Request) string {
	key, ok := auth.FromContext(r.Context())
	if !ok {
		return ""
	}
	return key.ID
}

// newProxyHandler builds the /proxy/ handler chain. With DATABASE_URL set,
// that's auth -> metrics -> rate limit -> proxy, with the API key from
// Postgres driving both the rate limit rule and the upstream target.
// Metrics sits after auth so it can attribute counters to a resolved key
// id; a request that fails auth never reaches it, since there's no key to
// attribute it to. Without DATABASE_URL, there's no auth at all and the
// chain falls back to the pre-multi-tenant behavior: one fixed upstream,
// one hardcoded rule, keyed by client IP (metrics uses the same IP key).
// The returned close func releases the Postgres pool, if one was opened;
// it's a no-op in the fallback case.
func newProxyHandler(cfg *config.Config, rl limiter.Limiter, reg *metrics.Registry) (http.Handler, func()) {
	if cfg.DatabaseURL == "" {
		log.Println("auth: DATABASE_URL not set, running in legacy single-upstream mode (no auth)")
		rule := limiter.Rule{Limit: 5, Window: 10 * time.Second, Burst: 5, Algo: cfg.LimiterAlgo}
		rateLimited := limiter.Middleware(rl, limiter.StaticRule(rule), clientKey)(proxy.New(proxy.Static(cfg.UpstreamURL)))
		handler := metrics.Middleware(reg, clientKey)(rateLimited)
		return handler, func() {}
	}

	connectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	pool, err := pgxpool.New(connectCtx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		log.Fatalf("connecting to Postgres: %v", err)
	}

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = pool.Ping(pingCtx)
	cancel()
	if err != nil {
		log.Fatalf("pinging Postgres: %v", err)
	}

	if err := store.RunMigrations(context.Background(), pool); err != nil {
		log.Fatalf("running migrations: %v", err)
	}

	keyStore := store.New(pool)
	cache := store.NewCache(keyStore, cacheTTL)

	log.Println("auth: using Postgres-backed API keys")

	rateLimited := limiter.Middleware(rl, ruleFromContext, keyIDFromContext)(proxy.New(upstreamFromContext))
	withMetrics := metrics.Middleware(reg, keyIDFromContext)(rateLimited)
	handler := auth.Middleware(cache)(withMetrics)

	return handler, pool.Close
}

// mountAdmin wires /admin/stats and the net/http/pprof endpoints, both
// behind AdminMiddleware. pprof handlers are added explicitly rather than
// via the usual blank import of net/http/pprof, because that package wires
// itself into http.DefaultServeMux — which this server doesn't use — not
// into a mux we control.
func mountAdmin(mux *http.ServeMux, reg *metrics.Registry, adminToken string) {
	adminAuth := auth.AdminMiddleware(adminToken)

	mux.Handle("/admin/stats", adminAuth(metrics.StatsHandler(reg)))

	mux.Handle("/debug/pprof/", adminAuth(http.HandlerFunc(pprof.Index)))
	mux.Handle("/debug/pprof/cmdline", adminAuth(http.HandlerFunc(pprof.Cmdline)))
	mux.Handle("/debug/pprof/profile", adminAuth(http.HandlerFunc(pprof.Profile)))
	mux.Handle("/debug/pprof/symbol", adminAuth(http.HandlerFunc(pprof.Symbol)))
	mux.Handle("/debug/pprof/trace", adminAuth(http.HandlerFunc(pprof.Trace)))
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	rl := newLimiter(cfg)
	defer func() {
		if err := rl.Close(); err != nil {
			log.Printf("closing rate limiter: %v", err)
		}
	}()

	reg := metrics.NewRegistry()

	proxyHandler, closeStore := newProxyHandler(cfg, rl, reg)
	defer closeStore()

	if cfg.AdminToken == "" {
		log.Println("admin: ADMIN_TOKEN not set, /admin/* and /debug/pprof/* are unreachable")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.Handle("/proxy/", proxyHandler)
	mountAdmin(mux, reg, cfg.AdminToken)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      loggingMiddleware(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		log.Printf("listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("graceful shutdown failed: %v", err)
	}

	log.Println("server stopped")
}
