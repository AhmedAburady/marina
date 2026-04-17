//go:build !windows

package ssh

import (
	"net"
	"os"
)

// dialSSHAgent dials the SSH agent socket.
// On Unix, the agent is reached via the SSH_AUTH_SOCK unix socket.
// Returns (nil, nil) when SSH_AUTH_SOCK is unset — agent auth is optional.
func dialSSHAgent() (net.Conn, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, nil
	}
	return net.Dial("unix", sock)
}
