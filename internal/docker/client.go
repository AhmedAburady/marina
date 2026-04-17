// Package docker provides a thin wrapper around the Docker Engine API client.
// Connections to remote hosts use Docker CLI's connhelper, which runs
// "ssh <host> docker system dial-stdio" under the hood.
package docker

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/docker/cli/cli/connhelper"
	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"

	internalssh "github.com/AhmedAburady/marina/internal/ssh"
)

// Client wraps a Docker Engine API client for a single remote host.
type Client struct {
	inner *dockerclient.Client
}

// NewClient creates a Docker client connected to the given SSH address
// (e.g. "ssh://user@10.0.0.50"). It uses Docker CLI's connhelper which
// shells out to the local ssh binary — the battle-tested production path.
//
// sshKeyPath is optional — when non-empty, it is passed as -i to ssh.
func NewClient(ctx context.Context, address string, sshKeyPath string) (*Client, error) {
	helper, err := connhelper.GetConnectionHelperWithSSHOpts(
		address,
		internalssh.Flags(internalssh.Config{KeyPath: sshKeyPath}),
	)
	if err != nil {
		return nil, fmt.Errorf("ssh connection helper for %s: %w", address, err)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext:     helper.Dialer,
			MaxConnsPerHost: 1, // Force all requests through one SSH pipe — prevents spawning multiple SSH subprocesses
		},
	}

	inner, err := dockerclient.NewClientWithOpts(
		dockerclient.WithHTTPClient(httpClient),
		dockerclient.WithHost(helper.Host),
		dockerclient.WithDialContext(helper.Dialer),
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("create docker client for %s: %w", address, err)
	}
	return &Client{inner: inner}, nil
}

// ListContainers returns all containers on the remote host (including stopped).
func (c *Client) ListContainers(ctx context.Context) ([]container.Summary, error) {
	containers, err := c.inner.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	return containers, nil
}

// ImageMeta captures what the registry check needs about a container's
// image. Digests is the full RepoDigests list filtered to the declared
// reference's repository — a list rather than a single string because
// Docker accumulates every registry digest that has ever pointed at the
// image's physical layers. For example, after `docker pull postgres:14`
// has run across two manifest-list revisions (identical amd64 bytes, but
// the list digest moved because the arm64 manifest changed), the image
// carries BOTH list digests. The registry check must match against any
// of them, not just the first — otherwise it reports "update available"
// forever because the first element is the stalest.
type ImageMeta struct {
	Ref          string   // image reference as declared (e.g. "postgres:14")
	Digests      []string // all matching RepoDigests, newest last
	Architecture string   // from image config, e.g. "amd64", "arm64"
	OS           string   // from image config, e.g. "linux"
}

// InspectContainer returns the image reference, every local digest that
// has ever been recorded for the underlying image, and the image's
// platform coordinates.
func (c *Client) InspectContainer(ctx context.Context, containerID string) (ImageMeta, error) {
	ctr, err := c.inner.ContainerInspect(ctx, containerID)
	if err != nil {
		return ImageMeta{}, fmt.Errorf("inspect container %s: %w", containerID, err)
	}

	meta := ImageMeta{Ref: ctr.Config.Image}

	img, _, err := c.inner.ImageInspectWithRaw(ctx, ctr.Image)
	if err != nil {
		return meta, fmt.Errorf("inspect image for %s: %w", containerID, err)
	}
	meta.Architecture = img.Architecture
	meta.OS = img.Os

	// RepoDigests looks like ["nginx@sha256:abc…", "ghcr.io/user/repo@sha256:def…"].
	// Keep every entry whose repository prefix matches the declared ref so
	// the caller can compare against any of them (old pulls + new pulls).
	repoPrefix := strings.Split(meta.Ref, ":")[0] + "@"
	for _, rd := range img.RepoDigests {
		if strings.HasPrefix(rd, repoPrefix) {
			meta.Digests = append(meta.Digests, rd)
		}
	}
	if len(meta.Digests) == 0 && len(img.RepoDigests) > 0 {
		// No prefix match (e.g. tag uses short name but digests use fully
		// qualified). Fall back to the full list rather than lose the info.
		meta.Digests = append(meta.Digests, img.RepoDigests...)
	}
	// Locally-built images leave Digests empty — the registry check treats
	// those as "no digest" and short-circuits.
	return meta, nil
}

// Close releases the underlying HTTP transport.
func (c *Client) Close() error {
	return c.inner.Close()
}
