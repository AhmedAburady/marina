package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// UpdateStatus represents whether an image has an update available.
type UpdateStatus int

const (
	UpToDate        UpdateStatus = iota
	UpdateAvailable
	CheckFailed
)

func (s UpdateStatus) String() string {
	switch s {
	case UpToDate:
		return "up-to-date"
	case UpdateAvailable:
		return "update available"
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

// CheckUpdate compares a local image digest against the remote registry.
// imageRef is the image reference (e.g. "nginx:latest", "ghcr.io/user/repo:tag").
// localDigest is the RepoDigest from Docker inspect (e.g. "sha256:abc123...").
//
// Returns UpdateAvailable if remote digest differs from local, UpToDate if same,
// or CheckFailed if the registry couldn't be reached.
func CheckUpdate(ctx context.Context, imageRef string, localDigest string) CheckResult {
	result := CheckResult{Image: imageRef, LocalDigest: localDigest}

	// Parse the image reference.
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		result.Status = CheckFailed
		result.Error = fmt.Errorf("parse image ref %q: %w", imageRef, err)
		return result
	}

	// Fetch remote descriptor (just the manifest digest, not the full image).
	desc, err := remote.Get(ref, remote.WithAuthFromKeychain(authn.DefaultKeychain), remote.WithContext(ctx))
	if err != nil {
		result.Status = CheckFailed
		result.Error = fmt.Errorf("fetch remote digest for %q: %w", imageRef, err)
		return result
	}

	result.RemoteDigest = desc.Digest.String()

	// Compare digests. The local digest from Docker might be in different formats.
	// Normalize: if localDigest contains "@", extract the digest part after "@".
	local := localDigest
	if idx := strings.LastIndex(local, "@"); idx >= 0 {
		local = local[idx+1:]
	}

	if local == result.RemoteDigest {
		result.Status = UpToDate
	} else {
		result.Status = UpdateAvailable
	}

	return result
}

// CheckUpdates checks multiple images in sequence (caller can parallelize).
func CheckUpdates(ctx context.Context, images map[string]string) []CheckResult {
	results := make([]CheckResult, 0, len(images))
	for imageRef, localDigest := range images {
		results = append(results, CheckUpdate(ctx, imageRef, localDigest))
	}
	return results
}
