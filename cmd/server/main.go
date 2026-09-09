package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/zoowen/gateway/internal/config"
	"github.com/zoowen/gateway/internal/limiter"
	"github.com/zoowen/gateway/internal/proxy"
)

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

func clientKey(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return fwd
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// newLimiter picks Redis when REDIS_URL is configured, otherwise falls back
// to the in-memory limiter (which only ever speaks token bucket — there's
// no in-memory sliding window implementation). It never falls back
// silently: a configured Redis that can't be reached, or an algorithm that
// has no in-memory equivalent, is a fatal error, not a quiet downgrade.
func newLimiter(cfg *config.Config) limiter.Limiter {
	if cfg.RedisURL == "" {
		if cfg.LimiterAlgo == config.AlgoSlidingWindow {
			log.Fatalf("LIMITER_ALGO=%s requires REDIS_URL (no in-memory implementation)", config.AlgoSlidingWindow)
		}
		log.Println("rate limiter: REDIS_URL not set, using in-memory token bucket")
		return limiter.NewMemory()
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

	switch cfg.LimiterAlgo {
	case config.AlgoSlidingWindow:
		log.Printf("rate limiter: using Redis sliding window at %s", opts.Addr)
		return limiter.NewSlidingWindow(client)
	default:
		log.Printf("rate limiter: using Redis token bucket at %s", opts.Addr)
		return limiter.NewRedis(client)
	}
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

	rateLimitRule := limiter.Rule{
		Limit:  5,
		Window: 10 * time.Second,
		Burst:  5,
	}
	proxyHandler := limiter.Middleware(rl, rateLimitRule, clientKey)(proxy.New(cfg.UpstreamURL))

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.Handle("/proxy/", proxyHandler)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      loggingMiddleware(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		log.Printf("listening on :%s, proxying to %s", cfg.Port, cfg.UpstreamURL)
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
