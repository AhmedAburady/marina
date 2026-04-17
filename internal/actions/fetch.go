package actions

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/docker"
	"github.com/AhmedAburady/marina/internal/state"
)

// HostFetchResult carries the outcome of a single-host container probe.
//
// Semantics:
//   - Live success:    Containers populated, FromCache=false, Err=nil.
//   - Cache fallback:  Containers populated from state cache, FromCache=true,
//     CachedAt set, Err=nil.
//   - Total failure:   Containers nil, FromCache=false, Err wraps the live
//     fetch error (cache lookup also failed or was absent).
type HostFetchResult struct {
	Host       string
	Containers []container.Summary
	FromCache  bool
	CachedAt   time.Time
	Err        error
}

// FetchAllHosts fans out to every target host concurrently, lists containers,
// and on per-host failure falls back to the state cache. Successful live
// fetches are written back to the cache best-effort.
//
// This is the single implementation both `marina ps`/`stacks` and the TUI
// Containers/Stacks screens rely on. Do NOT reimplement — if you need a
// slightly different shape, add a new function here instead.
func FetchAllHosts(
	ctx context.Context,
	cfg *config.Config,
	targets map[string]*config.HostConfig,
) map[string]HostFetchResult {
	results := make(map[string]HostFetchResult, len(targets))
	if len(targets) == 0 {
		return results
	}

	// Build a flat slice so FanOut can range over it cleanly.
	type hostEntry struct {
		name, address, sshKey string
	}
	entries := make([]hostEntry, 0, len(targets))
	for name, h := range targets {
		entries = append(entries, hostEntry{
			name:    name,
			address: h.SSHAddress(cfg.Settings.Username),
			sshKey:  h.ResolvedSSHKey(cfg.Settings.SSHKey),
		})
	}

	for r := range FanOut(ctx, entries, 0, func(ctx context.Context, e hostEntry) HostFetchResult {
		return fetchOneHost(ctx, e.name, e.address, e.sshKey)
	}) {
		results[r.Host] = r
	}

	// Persist all live-success snapshots in one atomic write: Load once, merge
	// every live result, Save once. Running this once per fetch (rather than
	// per-goroutine) avoids concurrent Load→modify→Save races on state.json.
	persistSnapshots(results)

	return results
}

// FetchHost fetches one host. Used by the TUI's streaming per-host fetch path.
// Persistence is handled by the caller (PersistResults) once all hosts have
// reported in, so we do one atomic write instead of one per host.
func FetchHost(ctx context.Context, cfg *config.Config, name string, h *config.HostConfig) HostFetchResult {
	return fetchOneHost(
		ctx,
		name,
		h.SSHAddress(cfg.Settings.Username),
		h.ResolvedSSHKey(cfg.Settings.SSHKey),
	)
}

// stateMu serialises access to state.json. Use RLock for reads, Lock for writes.
var stateMu sync.RWMutex

// PersistResults writes live-success fetch results to the state cache in one
// Load→merge→Save cycle. Called by TUI screens once their streaming fetch
// completes (received == expected) so we do one write instead of N.
func PersistResults(results map[string]HostFetchResult) {
	persistSnapshots(results)
}

// persistSnapshots merges live-fetch results into the state cache in a single
// Load→merge→Save cycle, avoiding concurrent write races.
func persistSnapshots(results map[string]HostFetchResult) {
	stateMu.Lock()
	defer stateMu.Unlock()
	// Collect only live-success results — do not overwrite cached entries and
	// do not clear existing snapshots for failed hosts.
	var live []HostFetchResult
	for _, r := range results {
		if r.Err == nil && !r.FromCache {
			live = append(live, r)
		}
	}
	if len(live) == 0 {
		return
	}

	store, err := state.Load("")
	if err != nil {
		store = &state.Store{Hosts: make(map[string]*state.HostSnapshot)}
	}
	now := time.Now()
	for _, r := range live {
		store.Hosts[r.Host] = &state.HostSnapshot{
			Containers: toStateContainers(r.Containers),
			UpdatedAt:  now,
		}
	}
	_ = state.Save(store, "")
}

// ── Internals ───────────────────────────────────────────────────────────────

func fetchOneHost(ctx context.Context, host, address, sshKey string) HostFetchResult {
	start := time.Now()
	defer func() {
		slog.Debug("fetch.host", "host", host, "elapsed", time.Since(start))
	}()

	containers, err := fetchLive(ctx, address, sshKey)
	if err != nil {
		if cached, cachedAt, ok := loadCachedContainers(host); ok {
			return HostFetchResult{Host: host, Containers: cached, FromCache: true, CachedAt: cachedAt}
		}
		return HostFetchResult{Host: host, Err: err}
	}
	// Persistence is handled by FetchAllHosts via persistSnapshots once all
	// goroutines complete.
	return HostFetchResult{Host: host, Containers: containers}
}

func fetchLive(ctx context.Context, address, sshKey string) ([]container.Summary, error) {
	c, err := docker.NewClient(ctx, address, sshKey)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer c.Close()
	containers, err := c.ListContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	return containers, nil
}

func loadCachedContainers(host string) ([]container.Summary, time.Time, bool) {
	stateMu.RLock()
	defer stateMu.RUnlock()
	store, err := state.Load("")
	if err != nil {
		return nil, time.Time{}, false
	}
	snap, ok := store.Hosts[host]
	if !ok || snap == nil {
		return nil, time.Time{}, false
	}
	return fromStateContainers(snap.Containers), snap.UpdatedAt, true
}

// toStateContainers maps Docker container summaries to cache-friendly state
// types. Single copy; previously duplicated in commands/ps.go and tui/loaders.go.
func toStateContainers(containers []container.Summary) []state.ContainerState {
	out := make([]state.ContainerState, len(containers))
	for i, c := range containers {
		var ports []state.PortState
		for _, p := range c.Ports {
			ports = append(ports, state.PortState{
				PublicPort:  p.PublicPort,
				PrivatePort: p.PrivatePort,
				Type:        p.Type,
			})
		}
		out[i] = state.ContainerState{
			ID:     c.ID,
			Names:  c.Names,
			Image:  c.Image,
			State:  c.State,
			Status: c.Status,
			Labels: c.Labels,
			Ports:  ports,
		}
	}
	return out
}

// fromStateContainers maps cached state entries back to Docker types.
func fromStateContainers(states []state.ContainerState) []container.Summary {
	out := make([]container.Summary, len(states))
	for i, s := range states {
		var ports []container.Port
		for _, p := range s.Ports {
			ports = append(ports, container.Port{
				PublicPort:  p.PublicPort,
				PrivatePort: p.PrivatePort,
				Type:        p.Type,
			})
		}
		out[i] = container.Summary{
			ID:     s.ID,
			Names:  s.Names,
			Image:  s.Image,
			State:  s.State,
			Status: s.Status,
			Labels: s.Labels,
			Ports:  ports,
		}
	}
	return out
}
