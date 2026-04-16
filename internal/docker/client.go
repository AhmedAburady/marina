// Package docker provides a thin wrapper around the Docker Engine API client.
// Connections to remote hosts use Docker CLI's connhelper, which runs
// "ssh <host> docker system dial-stdio" under the hood.
package docker

import (
	"context"
	"fmt"
	"net/http"

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
