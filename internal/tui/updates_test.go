package tui

import (
	"context"
	"errors"
	"testing"

	"charm.land/bubbles/v2/spinner"

	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/registry"
)

// newApplyingScreen builds an updatesScreen parked in the apply phase with
// three selected single-host stacks (a, b, c). pendingApply() therefore
// returns 3 and one valid host ("h") is configured so the prune path can run.
func newApplyingScreen(prune bool) *updatesScreen {
	s := &updatesScreen{
		ctx: context.Background(),
		cfg: &config.Config{
			Settings: config.Settings{PruneAfterUpdate: prune},
			Hosts:    map[string]*config.HostConfig{"h": {Address: "10.0.0.1"}},
		},
		phase:    phaseUpdatesApplying,
		spinner:  spinner.New(),
		selected: map[int]bool{0: true, 1: true, 2: true},
	}
	for _, stack := range []string{"a", "b", "c"} {
		s.results = append(s.results, registry.Result{
			Candidate: registry.Candidate{Host: "h", Stack: stack, Dir: "/opt/" + stack},
			HasUpdate: true,
		})
	}
	return s
}

// stackApplyResult mirrors one stack's pull+up sequence: a single
// SequenceResultsMsg carrying two step results. This is the shape that tripped
// the old step-counting completion logic.
func stackApplyResult(ok bool) SequenceResultsMsg {
	var upErr error
	if !ok {
		upErr = errors.New("compose up failed")
	}
	return SequenceResultsMsg{Results: []ActionResultMsg{
		{Kind: "compose.pull"},
		{Kind: "compose.up", Err: upErr},
	}}
}

// A multi-stack apply must wait for every stack's pull+up before rechecking.
// The regression: each stack emits 2 step results, so step-counting crossed
// pendingApply() after ~half the stacks and fired the recheck early — then
// stragglers re-fired it, cycling between the progress bar and a partial list.
func TestUpdatesApplyWaitsForAllStacks(t *testing.T) {
	s := newApplyingScreen(false)

	// First two stacks report — still applying, no premature recheck. The
	// count must track stacks (1 each), not steps (2 each); the old per-step
	// logic would read 4 here and have already crossed pendingApply()==3.
	for i := 1; i <= 2; i++ {
		s.Update(stackApplyResult(true))
		if s.phase != phaseUpdatesApplying {
			t.Fatalf("after %d/3 stacks: phase = %v, want phaseUpdatesApplying", i, s.phase)
		}
		if s.appliedOk != i {
			t.Fatalf("after %d/3 stacks: appliedOk = %d, want %d (one per stack, not per step)", i, s.appliedOk, i)
		}
	}

	// Third stack completes the set → recheck (back to loading). finishAndRecheck
	// resets the counters, so the phase transition is the completion signal here.
	s.Update(stackApplyResult(true))
	if s.phase != phaseUpdatesLoading {
		t.Fatalf("after 3/3 stacks: phase = %v, want phaseUpdatesLoading", s.phase)
	}

	// A straggler arriving after completion must be ignored — no second
	// recheck loop. finishAndRecheck cleared results, so pendingApply() is now
	// 0; without the stale-phase guard this message would re-fire the recheck.
	s.appliedOk = 0
	s.Update(stackApplyResult(true))
	if s.phase != phaseUpdatesLoading {
		t.Fatalf("stray result re-triggered work: phase = %v, want phaseUpdatesLoading", s.phase)
	}
	if s.appliedOk != 0 {
		t.Errorf("stray result was counted: appliedOk = %d, want 0", s.appliedOk)
	}
}

// With PruneAfterUpdate on, completing the apply set transitions into the
// prune phase (still "applying" visually); the recheck only fires once every
// per-host prune has reported.
func TestUpdatesApplyThenPruneThenRecheck(t *testing.T) {
	s := newApplyingScreen(true)

	for i := 0; i < 3; i++ {
		s.Update(stackApplyResult(true))
	}
	if !s.pruning {
		t.Fatal("after apply set with PruneAfterUpdate: pruning = false, want true")
	}
	if s.phase != phaseUpdatesApplying {
		t.Fatalf("during prune: phase = %v, want phaseUpdatesApplying", s.phase)
	}

	// One prune result per host (single host "h" here) finishes the cycle.
	s.Update(SequenceResultsMsg{Results: []ActionResultMsg{{Kind: "image.prune"}}})
	if s.phase != phaseUpdatesLoading {
		t.Fatalf("after prune: phase = %v, want phaseUpdatesLoading", s.phase)
	}
}
