package metrics

import (
	"sync"
	"testing"
	"time"
)

func TestRegistry_Record(t *testing.T) {
	r := NewRegistry()

	r.Record("key-a", true, 10*time.Millisecond)
	r.Record("key-a", true, 20*time.Millisecond)
	r.Record("key-a", false, 5*time.Millisecond)

	snap := r.Snapshot()
	stats, ok := snap.Keys["key-a"]
	if !ok {
		t.Fatal("expected key-a to be present in the snapshot")
	}
	if stats.Total != 3 || stats.Allowed != 2 || stats.Denied != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if snap.Latency.Count != 3 {
		t.Fatalf("expected latency count 3, got %d", snap.Latency.Count)
	}
}

func TestRegistry_KeysAreIndependent(t *testing.T) {
	r := NewRegistry()

	r.Record("key-a", true, time.Millisecond)
	r.Record("key-b", false, time.Millisecond)
	r.Record("key-b", false, time.Millisecond)

	snap := r.Snapshot()

	a := snap.Keys["key-a"]
	if a.Total != 1 || a.Allowed != 1 || a.Denied != 0 {
		t.Fatalf("key-a: unexpected stats: %+v", a)
	}

	b := snap.Keys["key-b"]
	if b.Total != 2 || b.Allowed != 0 || b.Denied != 2 {
		t.Fatalf("key-b: unexpected stats: %+v", b)
	}
}

// 100 goroutines record against the same key concurrently. Every counter
// must land exactly on 100 — this is what the atomic fields (and sync.Map
// for the key entry itself) buy over a plain map+mutex done wrong.
func TestRegistry_ConcurrentRecordSameKey(t *testing.T) {
	r := NewRegistry()

	const goroutines = 100
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r.Record("shared-key", i%2 == 0, time.Millisecond)
		}(i)
	}
	close(start)
	wg.Wait()

	stats := r.Snapshot().Keys["shared-key"]
	if stats.Total != goroutines {
		t.Fatalf("expected total %d, got %d", goroutines, stats.Total)
	}
	if stats.Allowed+stats.Denied != goroutines {
		t.Fatalf("expected allowed+denied to sum to %d, got %d+%d", goroutines, stats.Allowed, stats.Denied)
	}
	if stats.Allowed != goroutines/2 || stats.Denied != goroutines/2 {
		t.Fatalf("expected an even split, got allowed=%d denied=%d", stats.Allowed, stats.Denied)
	}
}

func TestRegistry_EmptySnapshot(t *testing.T) {
	r := NewRegistry()
	snap := r.Snapshot()

	if len(snap.Keys) != 0 {
		t.Fatalf("expected no keys, got %v", snap.Keys)
	}
	if snap.Latency.Count != 0 {
		t.Fatalf("expected latency count 0, got %d", snap.Latency.Count)
	}
}
