package actions

import (
	"context"
	"iter"
	"sync"
)

// FanOut runs fn concurrently over items and yields results as they arrive
// (completion order, not input order). Early termination (yield returning
// false) and context cancellation are both safe: the internal channel is
// buffered to len(items) so in-flight goroutines always have capacity to
// write their result and exit without being drained by the caller.
//
// limit <= 0 means unbounded (one goroutine per item). A positive limit caps
// the number of goroutines that may be running simultaneously via a buffered
// semaphore channel; acquire selects on ctx.Done() so a cancelled context
// unblocks waiting workers immediately.
func FanOut[T any, R any](ctx context.Context, items []T, limit int, fn func(context.Context, T) R) iter.Seq[R] {
	return func(yield func(R) bool) {
		if len(items) == 0 {
			return
		}

		ch := make(chan R, len(items))

		// sem is a counting semaphore implemented as a buffered channel.
		// nil means unbounded — no acquire/release needed.
		var sem chan struct{}
		if limit > 0 {
			sem = make(chan struct{}, limit)
		}

		var wg sync.WaitGroup
	loop:
		for _, item := range items {
			item := item

			if sem != nil {
				// Block until a slot is free or ctx is cancelled. If ctx is
				// done, stop dispatching — workers already launched will still
				// drain via wg.Wait in the shepherd below.
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					break loop
				}
			}

			wg.Add(1)
			go func() {
				defer wg.Done()
				if sem != nil {
					defer func() { <-sem }()
				}
				ch <- fn(ctx, item)
			}()
		}

		// Shepherd: close the result channel once all workers are done.
		go func() {
			wg.Wait()
			close(ch)
		}()

		// Drain the result channel, honouring ctx cancellation and early
		// termination from the caller returning false from yield.
		for {
			select {
			case r, ok := <-ch:
				if !ok {
					return
				}
				if !yield(r) {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}
}
