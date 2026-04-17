// Package ssh runs commands on remote hosts by shelling out to the system
// ssh binary. Single source of truth for SSH flags used by both direct
// command execution (Exec / Stream) and Docker's connhelper transport.
package ssh

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Config holds SSH connection parameters.
type Config struct {
	Address string // e.g. "ssh://user@host" or "user@host"
	KeyPath string // optional -i flag
}

// Flags returns the -o / -i arguments passed to the system ssh binary for
// every marina SSH call, regardless of caller (Exec, Stream, connhelper).
func Flags(cfg Config) []string {
	out := []string{
		"-o", "ConnectTimeout=5",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=~/.ssh/known_hosts",
		"-o", "BatchMode=yes",
	}
	if cfg.KeyPath != "" {
		out = append(out, "-i", cfg.KeyPath, "-o", "IdentitiesOnly=yes")
	}
	return out
}

// target strips any "ssh://" scheme and returns the "[user@]host[:port]"
// form ssh expects. If a :port is present we also emit -p <port>.
func target(cfg Config) (host string, portArgs []string) {
	addr := strings.TrimPrefix(cfg.Address, "ssh://")
	at := strings.LastIndex(addr, "@")
	user, rest := "", addr
	if at >= 0 {
		user, rest = addr[:at], addr[at+1:]
	}
	port := ""
	if i := strings.LastIndex(rest, ":"); i >= 0 && !strings.Contains(rest, "]") {
		rest, port = rest[:i], rest[i+1:]
	}
	host = rest
	if user != "" {
		host = user + "@" + host
	}
	if port != "" {
		portArgs = []string{"-p", port}
	}
	return
}

func sshArgs(cfg Config) []string {
	host, portArgs := target(cfg)
	args := Flags(cfg)
	args = append(args, portArgs...)
	args = append(args, host)
	return args
}

// Exec runs command on the remote host and returns combined stdout+stderr.
func Exec(ctx context.Context, cfg Config, command string) (string, error) {
	args := append(sshArgs(cfg), command)
	out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return string(out), fmt.Errorf("ssh exec: %w", ctx.Err())
		}
		return string(out), fmt.Errorf("ssh exec %q: %w", command, err)
	}
	return string(out), nil
}

// Stream runs command on the remote host, copying stdout and stderr to the
// provided writers in real time. Blocks until the command completes or ctx
// is cancelled.
func Stream(ctx context.Context, cfg Config, command string, stdout, stderr io.Writer) error {
	args := append(sshArgs(cfg), command)
	c := exec.CommandContext(ctx, "ssh", args...)
	c.Stdout = stdout
	c.Stderr = stderr
	if err := c.Run(); err != nil {
		if ctx.Err() != nil {
			return nil // caller cancelled — clean exit
		}
		return fmt.Errorf("ssh stream %q: %w", command, err)
	}
	return nil
}
