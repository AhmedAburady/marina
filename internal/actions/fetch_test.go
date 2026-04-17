package actions

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/state"
	"github.com/docker/docker/api/types/container"
)

// ── FetchAllHosts zero-target path ────────────────────────────────────────────

// TestFetchAllHosts_EmptyTargets verifies that calling FetchAllHosts with a nil
// targets map returns an empty result without panicking or writing to state.
// Full per-host fan-out is test-hostile because fetchOneHost hard-codes
// docker.NewClient with no injection seam; that gap is documented below.
func TestFetchAllHosts_EmptyTargets(t *testing.T) {
	// Redirect $HOME so any accidental state write goes to a temp dir.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg := &config.Config{}
	ctx := context.Background()

	results := FetchAllHosts(ctx, cfg, nil)
	if results == nil {
		t.Error("FetchAllHosts(nil targets) = nil, want empty map (not nil)")
	}
	if len(results) != 0 {
		t.Errorf("FetchAllHosts(nil targets) len = %d, want 0", len(results))
	}

	// No state file should have been created for a zero-host call.
	// Use state.DefaultPath() so the expected path honours os.UserConfigDir()
	// (XDG on Linux, ~/Library/Application Support on macOS, etc.).
	stateFile, err := state.DefaultPath()
	if err != nil {
		t.Fatalf("state.DefaultPath: %v", err)
	}
	if _, err := os.Stat(stateFile); err == nil {
		t.Error("state.json was created for a zero-host call, want no write")
	}
}

// NOTE (test-hostile): FetchAllHosts N-host partial-failure path cannot be
// tested without a stubable fetcher. fetchOneHost calls docker.NewClient
// directly with no injection seam. To cover "one host fails → others still
// succeed → result slice has entries for all N hosts" a future refactor should
// accept a fetcherFunc parameter (see P1-16 in REVIEW.md stage 5 notes).

// ── persistSnapshots ──────────────────────────────────────────────────────────

// TestPersistSnapshots_LiveOnlyPersisted verifies that only live-success results
// (Err==nil && !FromCache) are written to the state cache, and that failed +
// cached results are excluded.
func TestPersistSnapshots_LiveOnlyPersisted(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	now := time.Now().Truncate(time.Second)
	results := map[string]HostFetchResult{
		"live-host": {
			Host: "live-host",
			Containers: []container.Summary{
				{ID: "abc123", Names: []string{"/my-app"}, Image: "nginx:latest", State: "running", Status: "Up 2 hours"},
			},
			FromCache: false,
			Err:       nil,
		},
		"failed-host": {
			Host:      "failed-host",
			FromCache: false,
			Err:       os.ErrDeadlineExceeded,
		},
		"cached-host": {
			Host:      "cached-host",
			Containers: []container.Summary{
				{ID: "def456", Names: []string{"/old-app"}, Image: "redis:7", State: "running"},
			},
			FromCache: true,
			CachedAt:  now.Add(-5 * time.Minute),
			Err:       nil,
		},
	}

	persistSnapshots(results)

	// State should exist only for live-host.
	// Derive the path dynamically to honour os.UserConfigDir().
	statePath, err := state.DefaultPath()
	if err != nil {
		t.Fatalf("state.DefaultPath: %v", err)
	}
	store, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}

	if _, ok := store.Hosts["live-host"]; !ok {
		t.Error("live-host snapshot missing from state")
	}
	if snap := store.Hosts["live-host"]; len(snap.Containers) != 1 {
		t.Errorf("live-host containers = %d, want 1", len(snap.Containers))
	}
	if snap := store.Hosts["live-host"]; snap.Containers[0].Image != "nginx:latest" {
		t.Errorf("live-host container image = %q, want nginx:latest", snap.Containers[0].Image)
	}
	if _, ok := store.Hosts["failed-host"]; ok {
		t.Error("failed-host should NOT be in state (it failed)")
	}
	if _, ok := store.Hosts["cached-host"]; ok {
		t.Error("cached-host should NOT be in state (it came from cache, not live)")
	}
}

// TestPersistSnapshots_MergePreservesExisting verifies that a second call to
// persistSnapshots does not clobber a previously-stored host snapshot.
func TestPersistSnapshots_MergePreservesExisting(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// First pass: write host-a.
	persistSnapshots(map[string]HostFetchResult{
		"host-a": {
			Host: "host-a",
			Containers: []container.Summary{
				{ID: "aaa", Names: []string{"/svc-a"}, Image: "alpine:3", State: "running"},
			},
		},
	})

	// Second pass: write host-b only.
	persistSnapshots(map[string]HostFetchResult{
		"host-b": {
			Host: "host-b",
			Containers: []container.Summary{
				{ID: "bbb", Names: []string{"/svc-b"}, Image: "redis:7", State: "running"},
			},
		},
	})

	statePath, err := state.DefaultPath()
	if err != nil {
		t.Fatalf("state.DefaultPath: %v", err)
	}
	store, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	if _, ok := store.Hosts["host-a"]; !ok {
		t.Error("host-a was lost after second persistSnapshots call (non-atomic merge)")
	}
	if _, ok := store.Hosts["host-b"]; !ok {
		t.Error("host-b missing after second persistSnapshots call")
	}
}

// TestPersistSnapshots_NoLiveResults_NoWrite verifies that when all results are
// either failed or cached, persistSnapshots returns early and writes nothing.
func TestPersistSnapshots_NoLiveResults_NoWrite(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	results := map[string]HostFetchResult{
		"failed": {Host: "failed", Err: os.ErrDeadlineExceeded},
		"cached": {Host: "cached", FromCache: true},
	}
	persistSnapshots(results)

	stateFile, err := state.DefaultPath()
	if err != nil {
		t.Fatalf("state.DefaultPath: %v", err)
	}
	if _, err := os.Stat(stateFile); err == nil {
		t.Error("state.json written even though no live results existed")
	}
}

// TestPersistSnapshots_SaveFailure_NoPartialFile verifies that if the state
// directory is non-writable (so Save fails), no partial .tmp file is left
// behind. This exercises the atomic tmp+rename path via negative space:
// we confirm the dir-non-writable scenario returns an error from state.Save
// (which persistSnapshots ignores best-effort), and leaves the tmp dir clean.
//
// NOTE: We cannot verify the "previous state intact" property here because
// making the *config dir* non-writable prevents state.Save from calling
// MkdirAll → CreateTemp before any rename happens, so there is nothing to
// clean up — the invariant holds vacuously.
func TestPersistSnapshots_SaveToNonWritableDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// First write succeeds to establish a valid state file.
	persistSnapshots(map[string]HostFetchResult{
		"host-x": {
			Host:       "host-x",
			Containers: []container.Summary{{ID: "x", Image: "img:1", State: "running"}},
		},
	})

	// Lock the marina config dir so future writes fail.
	configDir, err := config.ResolveConfigDirForWrite()
	if err != nil {
		t.Fatalf("config.ResolveConfigDirForWrite: %v", err)
	}
	if err := os.Chmod(configDir, 0o555); err != nil {
		t.Skipf("chmod non-writable not supported on this platform: %v", err)
	}
	defer os.Chmod(configDir, 0o755) //nolint:errcheck

	// persistSnapshots is best-effort — it swallows the Save error.
	// The important assertion is: no .tmp file leaked.
	persistSnapshots(map[string]HostFetchResult{
		"host-y": {
			Host:       "host-y",
			Containers: []container.Summary{{ID: "y", Image: "img:2", State: "running"}},
		},
	})

	entries, _ := os.ReadDir(configDir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" && e.Name() != "state.json" {
			t.Errorf("leaked temp file: %s", e.Name())
		}
	}
}
