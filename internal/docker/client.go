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
	var sshFlags []string
	if sshKeyPath != "" {
		sshFlags = append(sshFlags, "-i", sshKeyPath)
	}

	helper, err := connhelper.GetConnectionHelperWithSSHOpts(address, sshFlags)
	if err != nil {
		return nil, fmt.Errorf("ssh connection helper for %s: %w", address, err)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: helper.Dialer,
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

// InspectContainer returns the image reference and local digest for a container.
// The digest comes from the image's RepoDigests (set when pulled from a registry).
func (c *Client) InspectContainer(ctx context.Context, containerID string) (imageRef string, digest string, err error) {
	// Inspect the container to get its ImageID
	ctr, err := c.inner.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", "", fmt.Errorf("inspect container %s: %w", containerID, err)
	}

	imageRef = ctr.Config.Image

	// Inspect the image to get RepoDigests
	img, _, err := c.inner.ImageInspectWithRaw(ctx, ctr.Image)
	if err != nil {
		return imageRef, "", fmt.Errorf("inspect image for %s: %w", containerID, err)
	}

	// RepoDigests looks like ["nginx@sha256:abc123...", "ghcr.io/user/repo@sha256:def456..."]
	// Return the first one that matches the image reference, or just the first one.
	for _, rd := range img.RepoDigests {
		if strings.HasPrefix(rd, strings.Split(imageRef, ":")[0]) {
			return imageRef, rd, nil
		}
	}
	if len(img.RepoDigests) > 0 {
		return imageRef, img.RepoDigests[0], nil
	}

	// No repo digest available (locally built image)
	return imageRef, "", nil
}

// Close releases the underlying HTTP transport.
func (c *Client) Close() error {
	return c.inner.Close()
}
