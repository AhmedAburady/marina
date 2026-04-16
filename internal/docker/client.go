// Package docker provides a thin wrapper around the Docker Engine API client.
// Each host gets its own Client connected via SSH transport — the Docker SDK
// handles the SSH tunnel natively through the "ssh://" scheme.
package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
)

// Client wraps a Docker Engine API client for a single remote host.
type Client struct {
	inner *dockerclient.Client
}

// NewClient creates a Docker client connected to the given SSH address
// (e.g. "ssh://user@10.0.0.50"). API version negotiation is enabled so
// marina works against any Docker Engine version the remote host runs.
func NewClient(ctx context.Context, address string) (*Client, error) {
	inner, err := dockerclient.NewClientWithOpts(
		dockerclient.WithHost(address),
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("create docker client for %s: %w", address, err)
	}
	return &Client{inner: inner}, nil
}

// ListContainers returns all running containers on the remote host.
func (c *Client) ListContainers(ctx context.Context) ([]container.Summary, error) {
	containers, err := c.inner.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	return containers, nil
}

// Close releases the underlying HTTP transport.
func (c *Client) Close() error {
	return c.inner.Close()
}
