package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func staticKeyFn(id string) func(*http.Request) string {
	return func(*http.Request) string { return id }
}

func TestMiddleware_RecordsAllowed(t *testing.T) {
	reg := NewRegistry()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := Middleware(reg, staticKeyFn("key-a"))(next)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/proxy/x", nil))

	stats := reg.Snapshot().Keys["key-a"]
	if stats.Total != 1 || stats.Allowed != 1 || stats.Denied != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestMiddleware_RecordsDenied(t *testing.T) {
	reg := NewRegistry()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})

	handler := Middleware(reg, staticKeyFn("key-a"))(next)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/proxy/x", nil))

	stats := reg.Snapshot().Keys["key-a"]
	if stats.Total != 1 || stats.Allowed != 0 || stats.Denied != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

// A downstream 502 (upstream unavailable) is not a 429: it must still
// count as "allowed" — the rate limiter let it through, the gateway just
// failed to reach the backend afterward.
func TestMiddleware_NonRateLimitErrorCountsAsAllowed(t *testing.T) {
	reg := NewRegistry()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	handler := Middleware(reg, staticKeyFn("key-a"))(next)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/proxy/x", nil))

	stats := reg.Snapshot().Keys["key-a"]
	if stats.Allowed != 1 || stats.Denied != 0 {
		t.Fatalf("expected a 502 to count as allowed, got %+v", stats)
	}
}

func TestMiddleware_DefaultStatusIsOK(t *testing.T) {
	reg := NewRegistry()
	// A handler that never calls WriteHeader explicitly still implies 200.
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	handler := Middleware(reg, staticKeyFn("key-a"))(next)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/proxy/x", nil))

	stats := reg.Snapshot().Keys["key-a"]
	if stats.Allowed != 1 || stats.Denied != 0 {
		t.Fatalf("expected implicit 200 to count as allowed, got %+v", stats)
	}
}

func TestStatsHandler_ReturnsJSON(t *testing.T) {
	reg := NewRegistry()
	reg.Record("key-a", true, 0)

	rec := httptest.NewRecorder()
	StatsHandler(reg).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/stats", nil))

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("expected a non-empty body")
	}
}
