package limiter

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

type middlewareConfig struct {
	failOpen bool
	logger   *slog.Logger
}

type Option func(*middlewareConfig)

// WithFailClosed rejects requests with 503 when the underlying Limiter's
// Allow() returns an error. Default behavior is fail-open.
func WithFailClosed() Option {
	return func(c *middlewareConfig) { c.failOpen = false }
}

func WithLogger(logger *slog.Logger) Option {
	return func(c *middlewareConfig) { c.logger = logger }
}

// StaticRule returns a ruleFn that always returns the same Rule, for
// callers that don't need a per-request (e.g. per-API-key) limit.
func StaticRule(rule Rule) func(*http.Request) Rule {
	return func(*http.Request) Rule { return rule }
}

func Middleware(l Limiter, ruleFn func(*http.Request) Rule, keyFn func(*http.Request) string, opts ...Option) func(http.Handler) http.Handler {
	cfg := middlewareConfig{
		failOpen: true,
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := keyFn(r)
			rule := ruleFn(r)

			decision, err := l.Allow(r.Context(), key, rule)
			if err != nil {
				if cfg.failOpen {
					cfg.logger.Warn("limiter allow error, failing open", "key", key, "error", err)
					next.ServeHTTP(w, r)
					return
				}
				cfg.logger.Warn("limiter allow error, failing closed", "key", key, "error", err)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate limiter unavailable"})
				return
			}

			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(decision.Limit))
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(decision.Remaining))
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(decision.ResetAt.Unix(), 10))

			if !decision.Allowed {
				retryAfter := int(decision.RetryAfter.Round(time.Second).Seconds())
				if retryAfter < 1 {
					retryAfter = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error":       "rate limit exceeded",
					"retry_after": retryAfter,
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
