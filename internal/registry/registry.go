package registry

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// sharedTransport is a package-level HTTP transport for all registry requests.
// It clones the default transport (preserving proxy + dial settings) and
// tightens timeout and connection-pool parameters to avoid hung registry calls.
var sharedTransport = func() *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.ResponseHeaderTimeout = 15 * time.Second
	base.TLSHandshakeTimeout = 10 * time.Second
	base.MaxIdleConnsPerHost = 4
	return base
}()

// UpdateStatus represents whether an image has an update available.
type UpdateStatus int

const (
	UpToDate        UpdateStatus = iota
	UpdateAvailable
	CheckFailed
	// Pinned signals an intentionally-versioned image reference
	// (e.g. `postgres:14`, `app:v1.2.3`, `image@sha256:…`). The user
	// chose that version on purpose, so we skip the registry probe
	// entirely and treat it the same as up-to-date for filtering.
	Pinned
)

func (s UpdateStatus) String() string {
	switch s {
	case UpToDate:
		return "up-to-date"
	case UpdateAvailable:
		return "update available"
	case Pinned:
		return "pinned"
	default:
		return "check failed"
	}
}

// CheckResult holds the result of an update check for a single image.
type CheckResult struct {
	Image        string       // e.g. "nginx:latest"
	Status       UpdateStatus
	LocalDigest  string // digest from docker inspect
	RemoteDigest string // digest from registry
	Error        error  // non-nil if check failed
}

// CheckUpdate compares a list of known local digests against the remote
// registry's current digest for a tag. Uses a HEAD request — this returns
// the registry's Docker-Content-Digest header, which for a multi-arch tag
// is the manifest list (index) digest. That's what Docker records in
// RepoDigests when you pull a tag.
//
// localDigests is a list because Docker accumulates every registry digest
// that has ever pointed at the same physical image. After successive pulls
// across manifest-list revisions the image may carry two or three digests;
// treating any of them as "current" prevents perpetual false positives
// when the list moves but the underlying platform-specific bytes don't.
func CheckUpdate(ctx context.Context, imageRef string, localDigests []string) CheckResult {
	result := CheckResult{Image: imageRef}
	if len(localDigests) > 0 {
		result.LocalDigest = localDigests[len(localDigests)-1] // newest for logging
	}

	ref, err := name.ParseReference(imageRef)
	if err != nil {
		result.Status = CheckFailed
		result.Error = fmt.Errorf("parse image ref %q: %w", imageRef, err)
		return result
	}

	desc, err := remote.Head(ref,
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
		remote.WithContext(ctx),
		remote.WithTransport(sharedTransport),
	)
	if err != nil {
		result.Status = CheckFailed
		result.Error = fmt.Errorf("fetch remote digest for %q: %w", imageRef, err)
		return result
	}
	result.RemoteDigest = desc.Digest.String()

	// Any local digest matching remote counts as up-to-date. Strip optional
	// `repo@` prefix before comparing.
	for _, ld := range localDigests {
		if idx := strings.LastIndex(ld, "@"); idx >= 0 {
			ld = ld[idx+1:]
		}
		if ld == result.RemoteDigest {
			result.Status = UpToDate
			return result
		}
	}
	result.Status = UpdateAvailable
	return result
}

// IsPinnedRef reports whether an image reference is pinned to an immutable
// digest (`…@sha256:…`). Digest pins are the only form of "pinned" that
// can be detected reliably from the reference alone — tag names carry no
// semantics about mutability (`:14` looks pinned but tracks patch releases;
// `:beta`, `:next`, `:stable` look custom but float; `:v1.2.3` is pinned
// in practice but indistinguishable from `:beta` by parsing). For any tag
// ref we defer to the registry check and let the user decide.
func IsPinnedRef(imageRef string) bool {
	return strings.Contains(imageRef, "@")
}

