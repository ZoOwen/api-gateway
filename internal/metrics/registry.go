package metrics

import (
	"sync"
	"sync/atomic"
	"time"
)

// keyCounters holds one API key's counters. Every field is atomic so
// incrementing them never needs a lock — this struct is looked up once per
// request and then hit on every single one, so contention here would
// contend the whole gateway.
type keyCounters struct {
	total   atomic.Int64
	allowed atomic.Int64
	denied  atomic.Int64
}

// Registry is the in-memory metrics store: per-API-key request counters
// plus one shared latency histogram. The per-key map uses sync.Map rather
// than a map guarded by an explicit mutex — new keys are rare (one insert
// per API key, ever) against a hot read/increment path, which is exactly
// what sync.Map is for. The histogram is the only place that takes an
// explicit mutex (see Histogram).
type Registry struct {
	perKey  sync.Map // string -> *keyCounters
	latency *Histogram
}

func NewRegistry() *Registry {
	return &Registry{latency: NewHistogram()}
}

func (r *Registry) counters(keyID string) *keyCounters {
	if v, ok := r.perKey.Load(keyID); ok {
		return v.(*keyCounters)
	}
	actual, _ := r.perKey.LoadOrStore(keyID, &keyCounters{})
	return actual.(*keyCounters)
}

// Record logs one completed request for keyID: total is always
// incremented, allowed xor denied is incremented depending on whether the
// rate limiter let it through, and latency is added to the shared
// histogram.
func (r *Registry) Record(keyID string, allowed bool, latency time.Duration) {
	c := r.counters(keyID)
	c.total.Add(1)
	if allowed {
		c.allowed.Add(1)
	} else {
		c.denied.Add(1)
	}
	r.latency.Observe(latency)
}

type KeyStats struct {
	Total   int64 `json:"total"`
	Allowed int64 `json:"allowed"`
	Denied  int64 `json:"denied"`
}

type LatencyStats struct {
	Count int64   `json:"count"`
	P50Ms float64 `json:"p50_ms"`
	P95Ms float64 `json:"p95_ms"`
	P99Ms float64 `json:"p99_ms"`
}

type Snapshot struct {
	Keys    map[string]KeyStats `json:"keys"`
	Latency LatencyStats        `json:"latency"`
}

func (r *Registry) Snapshot() Snapshot {
	keys := make(map[string]KeyStats)
	r.perKey.Range(func(k, v any) bool {
		c := v.(*keyCounters)
		keys[k.(string)] = KeyStats{
			Total:   c.total.Load(),
			Allowed: c.allowed.Load(),
			Denied:  c.denied.Load(),
		}
		return true
	})

	toMs := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

	return Snapshot{
		Keys: keys,
		Latency: LatencyStats{
			Count: r.latency.Count(),
			P50Ms: toMs(r.latency.Percentile(50)),
			P95Ms: toMs(r.latency.Percentile(95)),
			P99Ms: toMs(r.latency.Percentile(99)),
		},
	}
}
