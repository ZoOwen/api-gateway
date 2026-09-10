package metrics

import (
	"testing"
	"time"
)

func TestHistogram_Empty(t *testing.T) {
	h := NewHistogram()

	if got := h.Count(); got != 0 {
		t.Fatalf("expected count 0, got %d", got)
	}
	if got := h.Percentile(50); got != 0 {
		t.Fatalf("expected percentile 0 on an empty histogram, got %v", got)
	}
}

// 50 observations land exactly on the 10ms bucket boundary, 50 land
// exactly on the 100ms boundary. Because they sit precisely at bucket
// edges, the linear-interpolation estimate resolves to exact values —
// this pins down the interpolation math itself, not just "roughly right".
func TestHistogram_PercentileEstimate(t *testing.T) {
	h := NewHistogram()

	for i := 0; i < 50; i++ {
		h.Observe(10 * time.Millisecond)
	}
	for i := 0; i < 50; i++ {
		h.Observe(100 * time.Millisecond)
	}

	if got := h.Count(); got != 100 {
		t.Fatalf("expected count 100, got %d", got)
	}

	if got := h.Percentile(50); got != 10*time.Millisecond {
		t.Fatalf("expected p50 = 10ms, got %v", got)
	}
	if got := h.Percentile(99); got != 99*time.Millisecond {
		t.Fatalf("expected p99 = 99ms, got %v", got)
	}
}

// All ten observations sit exactly on the 5ms bucket boundary (the bucket
// covering (2ms, 5ms]). Linear interpolation within that bucket means only
// p100 (whose target is the full count) resolves to exactly the bucket's
// upper edge; lower percentiles legitimately land somewhere inside the
// bucket's range instead — that's the approximation, not a bug.
func TestHistogram_AllSameValue(t *testing.T) {
	h := NewHistogram()
	for i := 0; i < 10; i++ {
		h.Observe(5 * time.Millisecond)
	}

	for _, p := range []float64{50, 95, 99, 100} {
		got := h.Percentile(p)
		if got < 2*time.Millisecond || got > 5*time.Millisecond {
			t.Fatalf("p%v: expected estimate within (2ms, 5ms], got %v", p, got)
		}
	}

	if got := h.Percentile(100); got != 5*time.Millisecond {
		t.Fatalf("expected p100 = 5ms exactly (target equals the full count), got %v", got)
	}
}

func TestHistogram_OverflowBucket(t *testing.T) {
	h := NewHistogram()
	h.Observe(60 * time.Second) // far past the largest bucket boundary (5000ms)

	got := h.Percentile(99)
	want := msToDuration(latencyBucketsMs[len(latencyBucketsMs)-1])
	if got != want {
		t.Fatalf("expected the +Inf bucket to report its lower edge %v, got %v", want, got)
	}
}

func TestHistogram_Mean(t *testing.T) {
	h := NewHistogram()
	h.Observe(10 * time.Millisecond)
	h.Observe(20 * time.Millisecond)

	// Mean is computed from the exact running sum, not the buckets, so
	// this isn't an estimate.
	if got := h.Mean(); got != 15*time.Millisecond {
		t.Fatalf("expected mean 15ms, got %v", got)
	}
}
