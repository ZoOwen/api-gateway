package metrics

import (
	"encoding/json"
	"net/http"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Middleware records one Registry entry per request: total/allowed/denied
// for keyFn(r), and the request's latency in the shared histogram.
// "Denied" specifically means the rate limiter rejected it (HTTP 429) —
// anything else that reached this middleware (2xx, a proxy-side 502, ...)
// counts as allowed, since that's the gateway's admission decision, not
// the ultimate outcome of the request.
func Middleware(reg *Registry, keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			reg.Record(keyFn(r), rec.status != http.StatusTooManyRequests, time.Since(start))
		})
	}
}

// StatsHandler serves the raw Registry snapshot as JSON. Mount it behind
// AdminMiddleware — it has no auth of its own.
func StatsHandler(reg *Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reg.Snapshot())
	}
}
