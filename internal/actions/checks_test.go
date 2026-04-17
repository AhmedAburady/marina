package actions

import (
	"context"
	"testing"
)

// ── RunChecks orchestration ───────────────────────────────────────────────────
//
// RunChecks depends on BuildChecker → gatherCandidates → listCandidatesFromHost
// → docker.NewClient over SSH. There is no injection seam for either the
// gatherer or the registry check function; both are called directly.
//
// Consequently, only the zero-target (degenerate) path is exercisable without
// live infrastructure. The following tests are documented as blocked:
//
//   - Concurrency cap ≤ 8: errgroup.SetLimit(8) is invoked in RunChecks but
//     cannot be observed without injecting a fake checkFn that counts concurrent
//     goroutines. The check closure in BuildChecker is not parameterizable.
//     To fix: accept a CheckFn parameter in RunChecks (or a BuildCheckerFunc).
//
//   - "Unreachable host → Result{Status: host unreachable}": the synthetic-row
//     loop at actions/checks.go:35-41 is reachable only when BuildChecker
//     returns non-empty hostErrs, which requires at least one SSH-dialing
//     goroutine to fail. No stub exists for that path.
//
// These gaps should be addressed in a follow-up that adds injection seams to
// RunChecks and BuildChecker (see P1-16, stage 5 gap notes).

// TestRunChecks_NilTargets_NoError verifies the zero-host path: nil targets
// yields an empty (or nil) result slice with no error.
func TestRunChecks_NilTargets_NoError(t *testing.T) {
	ctx := context.Background()
	results, err := RunChecks(ctx, nil, nil)
	if err != nil {
		t.Fatalf("RunChecks(nil, nil) error = %v, want nil", err)
	}
	// May return nil or an empty slice — both are valid "no results".
	if len(results) != 0 {
		t.Errorf("RunChecks(nil, nil) results = %d, want 0", len(results))
	}
}
