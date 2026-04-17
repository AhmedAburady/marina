//go:build windows

package ssh

import (
	"net"

	"github.com/Microsoft/go-winio"
)

// dialSSHAgent dials the SSH agent.
// On Windows, OpenSSH agent uses a named pipe regardless of SSH_AUTH_SOCK.
func dialSSHAgent() (net.Conn, error) {
	return winio.DialPipe(`\\.\pipe\openssh-ssh-agent`, nil)
}
