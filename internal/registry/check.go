package registry

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/docker"
)

// Candidate describes a container eligible for an update check. It is the
// single shared shape consumed by both the dashboard's Updates tab and any
// future caller of BuildChecker.
type Candidate struct {
	Host         string
	Stack        string
	Container    string
	ContainerID  string
	Image        string
	ImageRef     string   // resolved image reference from inspect
	Digests      []string // all local RepoDigests for the image (newest last); empty = locally built
	Architecture string   // image platform arch, e.g. "amd64"
	OS           string   // image platform OS, e.g. "linux"
	Dir          string
}

// Result wraps a Candidate with its check outcome. Consumed by the Updates
// tab to drive row rendering + selection state.
type Result struct {
	Candidate
	Status       string
	HasUpdate    bool
	RemoteDigest string // platform-resolved digest from the registry; useful for debugging false positives
	Error        error
}

// CheckFn runs one registry check for a candidate. Safe for concurrent use:
// the returned function dedups concurrent calls for the same image and
// consults the persistent file cache before hitting the network.
type CheckFn func(ctx context.Context, c Candidate) Result

// BuildChecker fans out to each target host, inspects containers to gather
// update candidates, and returns a closure that performs one registry check
// per candidate. Every call hits the registry fresh — no persistent cache,
// no TTL — so rows always reflect current reality. The only dedup is an
// in-cycle sync.Map that prevents multiple containers sharing one image
// (e.g. three services on `postgres:14`) from issuing the same HEAD N times
// within a single check pass.
//
// Per-host failures are accepted as partial results: unreachable hosts are
// returned in hostErrs and the caller synthesises error Result rows for them.
// The check pass is never aborted due to a single host being unreachable.
func BuildChecker(
	ctx context.Context,
	cfg *config.Config,
	targets map[string]*config.HostConfig,
) (candidates []Candidate, check CheckFn, hostErrs map[string]error, err error) {
	candidates, hostErrs = gatherCandidates(ctx, cfg, targets)

	// In-cycle dedup only — each check pass starts with an empty map so
	// concurrent containers sharing an image share one HEAD, but the NEXT
	// pass re-queries everything fresh.
	type cacheEntry struct {
		result CheckResult
		done   chan struct{}
	}
	inflight := &sync.Map{}

	checkFn := func(ctx context.Context, c Candidate) Result {
		// Pinned refs (digest pins) are an intentional version choice —
		// skip the probe entirely. HasUpdate stays false so they fall out
		// of the default "updates only" view.
		if IsPinnedRef(c.ImageRef) {
			return Result{
				Candidate: c,
				Status:    Pinned.String(),
				HasUpdate: false,
			}
		}

		if len(c.Digests) == 0 {
			return Result{
				Candidate: c,
				Status:    "check failed",
				Error:     fmt.Errorf("no registry digest (locally built image)"),
			}
		}

		// Dedup key is imageRef + first local digest so two containers
		// sharing an image on the same host share one HEAD, but the same
		// image on a DIFFERENT host (different pull history → different
		// local digest list) runs its own check and gets its own compare.
		key := c.ImageRef + "|" + c.Digests[0]
		entry := &cacheEntry{done: make(chan struct{})}
		if existing, loaded := inflight.LoadOrStore(key, entry); loaded {
			e := existing.(*cacheEntry)
			<-e.done
			return Result{
				Candidate:    c,
				Status:       e.result.Status.String(),
				HasUpdate:    e.result.Status == UpdateAvailable,
				RemoteDigest: e.result.RemoteDigest,
				Error:        e.result.Error,
			}
		}

		cr := CheckUpdate(ctx, c.ImageRef, c.Digests)
		entry.result = cr
		close(entry.done)

		return Result{
			Candidate:    c,
			Status:       cr.Status.String(),
			HasUpdate:    cr.Status == UpdateAvailable,
			RemoteDigest: cr.RemoteDigest,
			Error:        cr.Error,
		}
	}

	return candidates, checkFn, hostErrs, nil
}

// ── candidate gathering ───────────────────────────────────────────────────────

// gatherCandidates fans out to all target hosts and collects lightweight
// Candidate entries (no registry check yet — just container list).
// Per-host failures are returned in the map keyed by host name; the
// successful candidates from reachable hosts are still returned so the
// caller can run a partial check pass rather than aborting the whole run.
func gatherCandidates(ctx context.Context, cfg *config.Config, targets map[string]*config.HostConfig) ([]Candidate, map[string]error) {
	type hostResult struct {
		host  string
		items []Candidate
		err   error
	}

	ch := make(chan hostResult, len(targets))
	var wg sync.WaitGroup

	for name, h := range targets {
		wg.Add(1)
		go func(hostName, address, sshKey string) {
			defer wg.Done()
			items, err := listCandidatesFromHost(ctx, hostName, address, sshKey)
			ch <- hostResult{host: hostName, items: items, err: err}
		}(name, h.SSHAddress(cfg.Settings.Username), h.ResolvedSSHKey(cfg.Settings.SSHKey))
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var all []Candidate
	var hostErrs map[string]error
	for r := range ch {
		if r.err != nil {
			if hostErrs == nil {
				hostErrs = make(map[string]error)
			}
			hostErrs[r.host] = fmt.Errorf("host %q: %w", r.host, r.err)
			continue
		}
		all = append(all, r.items...)
	}

	return all, hostErrs
}

// listCandidatesFromHost connects to one host and returns all containers as
// Candidate entries. Inspects each unique image (not each container) to
// minimize Docker API calls over the SSH pipe.
func listCandidatesFromHost(ctx context.Context, hostName, address, sshKey string) ([]Candidate, error) {
	dc, err := docker.NewClient(ctx, address, sshKey)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer dc.Close()

	containers, err := dc.ListContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	// Skip non-running containers entirely — there's no point hitting the
	// registry for an image whose only consumer is exited/dead/created.
	// Stacks where every service is stopped fall out of the list naturally
	// because all of their containers get filtered here.
	//
	// Inspect each unique *running* image concurrently — transport serializes
	// via MaxConnsPerHost: 1.
	cache := make(map[string]docker.ImageMeta)
	var mu sync.Mutex
	var wg sync.WaitGroup

	seen := make(map[string]bool)
	for _, c := range containers {
		if c.State != "running" {
			continue
		}
		if seen[c.ImageID] {
			continue
		}
		seen[c.ImageID] = true
		wg.Add(1)
		go func(id, fallbackImage string, cID string) {
			defer wg.Done()
			meta, err := dc.InspectContainer(ctx, cID)
			mu.Lock()
			if err != nil && meta.Ref == "" {
				meta.Ref = fallbackImage
			}
			cache[id] = meta
			mu.Unlock()
		}(c.ImageID, c.Image, c.ID)
	}
	wg.Wait()

	out := make([]Candidate, 0, len(containers))
	for _, c := range containers {
		if c.State != "running" {
			continue
		}
		meta := cache[c.ImageID]
		ref := meta.Ref
		if ref == "" {
			ref = c.Image
		}
		out = append(out, Candidate{
			Host:         hostName,
			Stack:        stackLabel(c.Labels),
			Container:    containerDisplayName(c.Names, c.ID),
			ContainerID:  c.ID,
			Image:        c.Image,
			ImageRef:     ref,
			Digests:      meta.Digests,
			Architecture: meta.Architecture,
			OS:           meta.OS,
			Dir:          c.Labels["com.docker.compose.project.working_dir"],
		})
	}
	return out, nil
}

// ── display helpers (duplicated from commands/updates.go; Phase 5 cleans up) ──

// stackLabel returns the compose project name from container labels, or "-".
func stackLabel(labels map[string]string) string {
	if s := labels["com.docker.compose.project"]; s != "" {
		return s
	}
	return "-"
}

// containerDisplayName returns the primary container name with the leading
// slash stripped, or a short ID when no name is available.
func containerDisplayName(names []string, id string) string {
	if len(names) > 0 {
		return strings.TrimPrefix(names[0], "/")
	}
	if len(id) >= 12 {
		return id[:12]
	}
	return id
}
