package actions

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestFanOut_Empty verifies that FanOut over an empty slice yields nothing
// and returns cleanly without blocking or panicking.
func TestFanOut_Empty(t *testing.T) {
	ctx := context.Background()
	count := 0
	for range FanOut(ctx, []int{}, 0, func(ctx context.Context, n int) int { return n }) {
		count++
	}
	if count != 0 {
		t.Errorf("FanOut(empty) yielded %d results, want 0", count)
	}
}

// TestFanOut_AllResults verifies that FanOut over N items yields exactly N
// results with no omissions.
func TestFanOut_AllResults(t *testing.T) {
	ctx := context.Background()
	items := []int{1, 2, 3, 4, 5}

	sum := 0
	for r := range FanOut(ctx, items, 0, func(ctx context.Context, n int) int { return n }) {
		sum += r
	}
	// 1+2+3+4+5 = 15
	if sum != 15 {
		t.Errorf("FanOut result sum = %d, want 15", sum)
	}
}

// TestFanOut_CtxCancelNoDeadlock verifies that cancelling ctx before the
// iterator is fully consumed does not cause a deadlock. Tested under -race.
func TestFanOut_CtxCancelNoDeadlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 20 slow items; we cancel after consuming the first result.
	items := make([]int, 20)
	for i := range items {
		items[i] = i
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		consumed := 0
		for range FanOut(ctx, items, 0, func(ctx context.Context, n int) int {
			time.Sleep(5 * time.Millisecond)
			return n
		}) {
			consumed++
			if consumed == 1 {
				cancel() // cancel after first result; do NOT keep consuming
				return
			}
		}
	}()

	select {
	case <-done:
		// clean exit — no deadlock
	case <-time.After(10 * time.Second):
		t.Fatal("FanOut deadlocked after ctx cancel")
	}
}

// TestFanOut_LimitConcurrency verifies that with limit=2 over 10 items, at
// most 2 workers run simultaneously. Uses an atomic counter + CAS to track the
// observed peak concurrency.
func TestFanOut_LimitConcurrency(t *testing.T) {
	ctx := context.Background()
	items := make([]int, 10)
	for i := range items {
		items[i] = i
	}

	var current atomic.Int32
	var peak atomic.Int32

	for range FanOut(ctx, items, 2, func(ctx context.Context, n int) int {
		cur := current.Add(1)
		// Update peak via CAS loop so we never miss a higher value.
		for {
			old := peak.Load()
			if cur <= old || peak.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond) // ensure overlap
		current.Add(-1)
		return n
	}) {
		// consume all results
	}

	if p := peak.Load(); p > 2 {
		t.Errorf("peak concurrency = %d, want <= 2", p)
	}
	if p := peak.Load(); p == 0 {
		t.Error("peak concurrency = 0, no workers ran")
	}
}
