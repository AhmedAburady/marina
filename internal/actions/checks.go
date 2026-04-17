package actions

import (
	"context"

	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/registry"
)

// RunChecks gathers update candidates, fans out registry HEAD requests with
// a concurrency cap of 8, and returns the results. Unreachable hosts are
// silently skipped — reachability surfaces only via `marina hosts`.
func RunChecks(
	ctx context.Context,
	cfg *config.Config,
	targets map[string]*config.HostConfig,
) ([]registry.Result, error) {
	candidates, check, _, err := registry.BuildChecker(ctx, cfg, targets)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	results := make([]registry.Result, 0, len(candidates))
	for r := range FanOut(ctx, candidates, 8, func(ctx context.Context, c registry.Candidate) registry.Result {
		return check(ctx, c)
	}) {
		results = append(results, r)
	}
	return results, nil
}
