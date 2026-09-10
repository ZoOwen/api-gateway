package metrics

import (
	"sort"
	"sync"
	"time"
)

// latencyBucketsMs are the upper bounds of each bucket, in milliseconds.
// There's an implicit final +Inf bucket for anything larger than the last
// one. This is a fixed, simple bucket scheme — not a library — chosen to
// cover sub-millisecond to multi-second gateway latencies.
var latencyBucketsMs = []float64{0.5, 1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

// Histogram is a bucketed latency histogram. Percentiles are estimated by
// linear interpolation within the bucket that contains the target rank —
// the same approximation Prometheus's histogram_quantile uses — not exact
// values computed from raw samples, which a bucketed histogram never
// stores.
type Histogram struct {
	mu      sync.Mutex
	buckets []int64 // counts; buckets[len(latencyBucketsMs)] is the +Inf bucket
	count   int64
	sum     time.Duration
}

func NewHistogram() *Histogram {
	return &Histogram{buckets: make([]int64, len(latencyBucketsMs)+1)}
}

func (h *Histogram) Observe(d time.Duration) {
	ms := float64(d) / float64(time.Millisecond)
	idx := sort.SearchFloat64s(latencyBucketsMs, ms)

	h.mu.Lock()
	h.buckets[idx]++
	h.count++
	h.sum += d
	h.mu.Unlock()
}

func (h *Histogram) Count() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

func (h *Histogram) Mean() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 {
		return 0
	}
	return h.sum / time.Duration(h.count)
}

// Percentile estimates the p-th percentile (0 < p <= 100) in milliseconds.
func (h *Histogram) Percentile(p float64) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.count == 0 {
		return 0
	}

	target := p / 100 * float64(h.count)

	var cumulative int64
	for i, c := range h.buckets {
		prevCumulative := cumulative
		cumulative += c
		if float64(cumulative) < target {
			continue
		}

		lower := 0.0
		if i > 0 {
			lower = latencyBucketsMs[i-1]
		}

		if i >= len(latencyBucketsMs) {
			// The +Inf bucket has no upper bound to interpolate against;
			// report its lower edge rather than fabricate one.
			return msToDuration(lower)
		}
		upper := latencyBucketsMs[i]

		if c == 0 {
			return msToDuration(lower)
		}
		frac := (target - float64(prevCumulative)) / float64(c)
		return msToDuration(lower + frac*(upper-lower))
	}

	return msToDuration(latencyBucketsMs[len(latencyBucketsMs)-1])
}

func msToDuration(ms float64) time.Duration {
	return time.Duration(ms * float64(time.Millisecond))
}
