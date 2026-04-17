package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	gcrreg "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// pushImageToHost pushes a random image to the registry at the given host
// (addr:port, plain HTTP) and returns the fully-qualified reference
// (host/repo:tag) and the image digest string.
func pushImageToHost(t *testing.T, host, repo, tag string) (ref string, digest string) {
	t.Helper()
	img, err := random.Image(512, 2)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	d, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}

	fullRef := host + "/" + repo + ":" + tag
	r, err := name.ParseReference(fullRef, name.Insecure)
	if err != nil {
		t.Fatalf("name.ParseReference(%q): %v", fullRef, err)
	}
	if err := remote.Write(r, img,
		remote.WithAuth(authn.Anonymous),
		remote.WithTransport(http.DefaultTransport),
	); err != nil {
		t.Fatalf("remote.Write: %v", err)
	}
	return fullRef, d.String()
}

// startFakeRegistry launches an httptest server backed by the ggcr in-process
// registry and patches package-level sharedTransport to allow plain HTTP so
// CheckUpdate can reach it. Returns the host address (addr:port).
func startFakeRegistry(t *testing.T) string {
	t.Helper()
	return startFakeRegistryWithHandler(t, gcrreg.New())
}

// startFakeRegistryWithHandler is like startFakeRegistry but wraps an arbitrary
// http.Handler (useful for counting requests).
func startFakeRegistryWithHandler(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	original := sharedTransport
	// Allow CheckUpdate to reach plain-HTTP httptest servers.
	sharedTransport = http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(func() {
		srv.Close()
		sharedTransport = original
	})
	return srv.Listener.Addr().String()
}

// buildDefaultTransport mirrors the package-level initialiser.
func buildDefaultTransport() *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.ResponseHeaderTimeout = 15 * time.Second
	base.TLSHandshakeTimeout = 10 * time.Second
	base.MaxIdleConnsPerHost = 4
	return base
}

// ── IsPinnedRef ───────────────────────────────────────────────────────────────

func TestIsPinnedRef(t *testing.T) {
	cases := []struct {
		ref    string
		pinned bool
	}{
		{"nginx:latest", false},
		{"postgres:14", false},
		{"myapp:v1.2.3", false},
		{"registry.example.com/app:stable", false},
		{"nginx@sha256:abc123def456", true},
		{"postgres@sha256:0000000000000000000000000000000000000000000000000000000000000000", true},
		{"registry.example.com/app@sha256:deadbeef", true},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			got := IsPinnedRef(tc.ref)
			if got != tc.pinned {
				t.Errorf("IsPinnedRef(%q) = %v, want %v", tc.ref, got, tc.pinned)
			}
		})
	}
}

// ── CheckUpdate ───────────────────────────────────────────────────────────────

// TestCheckUpdate_UpToDate verifies UpToDate when local digest matches remote.
func TestCheckUpdate_UpToDate(t *testing.T) {
	host := startFakeRegistry(t)
	ref, digest := pushImageToHost(t, host, "myapp", "latest")

	// Simulate Docker's RepoDigests format: "repo@sha256:..."
	repoPrefix := ref[:len(ref)-len(":latest")]
	localDigest := repoPrefix + "@" + digest

	ctx := context.Background()
	result := CheckUpdate(ctx, ref, []string{localDigest})

	if result.Status != UpToDate {
		t.Errorf("Status = %s, want UpToDate; error: %v", result.Status, result.Error)
	}
}

// TestCheckUpdate_UpdateAvailable verifies UpdateAvailable when local digest
// does not match the remote.
func TestCheckUpdate_UpdateAvailable(t *testing.T) {
	host := startFakeRegistry(t)
	ref, _ := pushImageToHost(t, host, "myapp2", "latest")

	staleDigest := "sha256:0000000000000000000000000000000000000000000000000000000000000000"

	ctx := context.Background()
	result := CheckUpdate(ctx, ref, []string{staleDigest})

	if result.Status != UpdateAvailable {
		t.Errorf("Status = %s, want UpdateAvailable; error: %v", result.Status, result.Error)
	}
}

// TestCheckUpdate_MultipleLocalDigests verifies that any matching local digest
// (not only the last) counts as up-to-date. Docker accumulates multiple
// RepoDigests across pulls; treating any as current prevents false positives.
func TestCheckUpdate_MultipleLocalDigests(t *testing.T) {
	host := startFakeRegistry(t)
	ref, digest := pushImageToHost(t, host, "myapp3", "latest")

	stale := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	repoPrefix := ref[:len(ref)-len(":latest")]
	current := repoPrefix + "@" + digest

	// current is first in the slice, not last — the loop should still find it.
	ctx := context.Background()
	result := CheckUpdate(ctx, ref, []string{current, stale})

	if result.Status != UpToDate {
		t.Errorf("Status = %s, want UpToDate", result.Status)
	}
}

// ── BuildChecker check closure ────────────────────────────────────────────────

// TestCheckFn_PinnedRefShortCircuit verifies that a digest-pinned ImageRef
// produces Status=="pinned", HasUpdate==false, and does NOT issue any HEAD
// to the registry.
func TestCheckFn_PinnedRefShortCircuit(t *testing.T) {
	var headCount atomic.Int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			headCount.Add(1)
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	pinnedRef := srv.Listener.Addr().String() + "/app@sha256:abc123def456abc123def456abc123def456abc123def456abc123def456abc123"

	if !IsPinnedRef(pinnedRef) {
		t.Fatalf("IsPinnedRef(%q) = false, want true", pinnedRef)
	}

	ctx := context.Background()
	// BuildChecker with empty targets produces a fully-functional check closure
	// without requiring any SSH host.
	_, checkFn, _, err := BuildChecker(ctx, nil, nil)
	if err != nil {
		t.Fatalf("BuildChecker: %v", err)
	}

	result := checkFn(ctx, Candidate{
		Host:     "test-host",
		Image:    "app",
		ImageRef: pinnedRef,
		Digests:  []string{"sha256:abc123"},
	})

	if result.Status != Pinned.String() {
		t.Errorf("Status = %q, want %q", result.Status, Pinned.String())
	}
	if result.HasUpdate {
		t.Error("HasUpdate should be false for pinned refs")
	}
	if n := headCount.Load(); n != 0 {
		t.Errorf("registry received %d HEAD requests, want 0 (pinned ref must not probe)", n)
	}
}

// TestCheckFn_EmptyDigests verifies that a Candidate with no local digests
// (locally-built image) returns Status=="check failed" with a non-nil Error.
func TestCheckFn_EmptyDigests(t *testing.T) {
	ctx := context.Background()
	_, checkFn, _, err := BuildChecker(ctx, nil, nil)
	if err != nil {
		t.Fatalf("BuildChecker: %v", err)
	}

	result := checkFn(ctx, Candidate{
		Host:     "test-host",
		Image:    "local-app",
		ImageRef: "local-app:latest",
		Digests:  nil,
	})

	if result.Status != CheckFailed.String() {
		t.Errorf("Status = %q, want %q", result.Status, CheckFailed.String())
	}
	if result.Error == nil {
		t.Error("Error should be non-nil for locally-built image with no digests")
	}
}

// TestCheckFn_InCycleDedup verifies that two Candidates sharing the same
// ImageRef and first Digest share a single HEAD request within one check pass.
// HEAD count is measured at the httptest handler.
func TestCheckFn_InCycleDedup(t *testing.T) {
	var headCount atomic.Int64
	inner := gcrreg.New()
	counting := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			headCount.Add(1)
		}
		inner.ServeHTTP(w, r)
	})

	host := startFakeRegistryWithHandler(t, counting)
	ref, digest := pushImageToHost(t, host, "shared", "latest")

	// Reset the counter after the push phase so we only count HEAD requests
	// that originate from checkFn calls below.
	headCount.Store(0)

	ctx := context.Background()
	_, checkFn, _, err := BuildChecker(ctx, nil, nil)
	if err != nil {
		t.Fatalf("BuildChecker: %v", err)
	}

	repoPrefix := ref[:len(ref)-len(":latest")]
	localDigest := repoPrefix + "@" + digest
	c1 := Candidate{Host: "host1", Image: "shared", ImageRef: ref, Digests: []string{localDigest}}
	c2 := Candidate{Host: "host1", Image: "shared", ImageRef: ref, Digests: []string{localDigest}}

	r1 := checkFn(ctx, c1)
	r2 := checkFn(ctx, c2)

	if r1.Status != UpToDate.String() {
		t.Errorf("c1 Status = %q, want %q", r1.Status, UpToDate.String())
	}
	if r2.Status != UpToDate.String() {
		t.Errorf("c2 Status = %q, want %q", r2.Status, UpToDate.String())
	}
	if n := headCount.Load(); n != 1 {
		t.Errorf("HEAD requests = %d, want 1 (in-cycle dedup)", n)
	}
}

// TestBuildChecker_EmptyTargets_NoError verifies the zero-host path: empty
// targets yields zero candidates, empty hostErrs, and nil top-level error.
// NOTE: the "host unreachable" Result rows visible in the TUI are synthesised
// by internal/actions/checks.go, not this package; testing that path requires
// a stubable docker.Client factory which does not exist yet (see
// listCandidatesFromHost in check.go:175). That gap is documented as a
// test-hostile design in the stage report.
func TestBuildChecker_EmptyTargets_NoError(t *testing.T) {
	ctx := context.Background()
	candidates, _, hostErrs, err := BuildChecker(ctx, nil, nil)
	if err != nil {
		t.Fatalf("BuildChecker: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %d, want 0", len(candidates))
	}
	if len(hostErrs) != 0 {
		t.Errorf("hostErrs = %v, want empty", hostErrs)
	}
}
