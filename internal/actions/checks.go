package actions

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/registry"
)

// RunChecks gathers update candidates from all target hosts, fans out
// registry HEAD requests with a concurrency cap of 8, and returns the
// full result set. Per-host gather failures are surfaced as synthetic
// Result rows with Status "host unreachable" rather than aborting the
// entire pass — reachable hosts still contribute results.
//
// This is the single implementation both `marina check`/`marina update`
// and the TUI Updates screen call through. Do NOT reimplement.
func RunChecks(
	ctx context.Context,
	cfg *config.Config,
	targets map[string]*config.HostConfig,
) ([]registry.Result, error) {
	candidates, check, hostErrs, err := registry.BuildChecker(ctx, cfg, targets)
	if err != nil {
		return nil, err
	}

	// Synthesise placeholder rows for unreachable hosts so callers always get
	// a complete picture — mirroring the pattern in internal/tui/containers.go
	// where unreachable hosts produce a "(unreachable)" placeholder row.
	var errRows []registry.Result
	for host, herr := range hostErrs {
		errRows = append(errRows, registry.Result{
			Candidate: registry.Candidate{Host: host, Container: "(unreachable)"},
			Status:    "host unreachable",
			Error:     fmt.Errorf("%w", herr),
		})
	}

	if len(candidates) == 0 {
		return errRows, nil
	}

	// Fan out HEAD requests with a concurrency cap of 8 so we don't
	// hammer Docker Hub with unbounded goroutines (P2-2).
	results := make([]registry.Result, len(candidates))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i, c := range candidates {
		i, c := i, c
		g.Go(func() error {
			results[i] = check(gctx, c)
			return nil // check never returns an error — errors live in Result.Error
		})
	}
	_ = g.Wait() // cannot fail: goroutines return nil

	return append(results, errRows...), nil
}
