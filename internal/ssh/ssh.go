// Package ssh provides SSH execution primitives for Marina's lifecycle commands.
// It supports key-based auth (unencrypted PEM files), SSH agent auth, and
// known_hosts verification — never insecure host key ignoring.
package ssh

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

const dialTimeout = 30 * time.Second

// Config holds SSH connection parameters.
type Config struct {
	// Address is the full SSH URL, e.g. "ssh://user@host" or "ssh://user@host:port".
	Address string
	// KeyPath is the path to an unencrypted PEM private key. Optional when an
	// SSH agent is available via SSH_AUTH_SOCK.
	KeyPath string
}

// parseAddress strips the "ssh://" scheme, splits off the user, and returns
// the user, host, and port (defaulting to 22 when no port is specified).
func parseAddress(address string) (user, host string, port int) {
	port = 22

	// Strip scheme.
	addr := strings.TrimPrefix(address, "ssh://")

	// Split user@rest.
	if at := strings.LastIndex(addr, "@"); at >= 0 {
		user = addr[:at]
		addr = addr[at+1:]
	}

	// addr is now "host", "host:port", "[::1]", or "[::1]:port".
	// net.SplitHostPort handles all bracketed IPv6 forms and plain host:port.
	if h, p, err := net.SplitHostPort(addr); err == nil {
		if n, err := strconv.Atoi(p); err == nil {
			return user, h, n
		}
	}

	// No port component — strip any brackets that denote a bare IPv6 address
	// (e.g. "[::1]" → "::1") and default port to 22.
	host = strings.Trim(addr, "[]")
	return
}

// newClientConfig builds an *ssh.ClientConfig for the given Config.
// It attempts SSH agent auth first (via SSH_AUTH_SOCK), then falls back to
// key-file auth if KeyPath is set. Known-hosts verification is always enforced.
func newClientConfig(cfg Config) (*ssh.ClientConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}

	// ── Host key verification ────────────────────────────────────────────────
	knownHostsPath := filepath.Join(home, ".ssh", "known_hosts")
	hostKeyCallback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(
				"known_hosts file not found at %s: "+
					"run `ssh-keyscan <host> >> %s` to add the remote host first",
				knownHostsPath, knownHostsPath,
			)
		}
		return nil, fmt.Errorf("load known_hosts %s: %w", knownHostsPath, err)
	}

	// ── Auth methods ─────────────────────────────────────────────────────────
	var authMethods []ssh.AuthMethod

	// 1. SSH agent (best option when available, but only if it has keys).
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		agentConn, err := net.Dial("unix", sock)
		if err == nil {
			agentClient := agent.NewClient(agentConn)
			// Only use agent auth if it actually has keys loaded.
			if keys, err := agentClient.List(); err == nil && len(keys) > 0 {
				authMethods = append(authMethods, ssh.PublicKeysCallback(agentClient.Signers))
			} else {
				agentConn.Close()
			}
		}
	}

	// 2. Key file auth.
	if cfg.KeyPath != "" {
		keyBytes, err := os.ReadFile(cfg.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("read SSH key %s: %w", cfg.KeyPath, err)
		}
		// Guard against world- or group-readable key files (OpenSSH refuses them).
		// Skip on Windows, which uses ACLs rather than Unix permission bits.
		if runtime.GOOS != "windows" {
			info, err := os.Stat(cfg.KeyPath)
			if err != nil {
				return nil, fmt.Errorf("stat SSH key %s: %w", cfg.KeyPath, err)
			}
			if perm := info.Mode().Perm(); perm&0o077 != 0 {
				return nil, fmt.Errorf(
					"ssh key %s has permissions %o; run chmod 600 %s",
					cfg.KeyPath, perm, cfg.KeyPath,
				)
			}
		}
		signer, err := ssh.ParsePrivateKey(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("parse SSH key %s: %w", cfg.KeyPath, err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf(
			"no SSH auth methods available: set SSH_AUTH_SOCK or provide a key path",
		)
	}

	user, _, _ := parseAddress(cfg.Address)

	// Log which auth methods will be attempted. x/crypto/ssh's AuthLogCallback
	// is server-side only; on the client we log at construction time.
	// Method names: "publickey" (agent or key file).
	slog.Debug("ssh auth methods configured",
		"host", cfg.Address,
		"method_count", len(authMethods),
	)

	clientCfg := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         dialTimeout,
	}
	return clientCfg, nil
}

// dial establishes an SSH connection to the host described by cfg.
// It honours ctx for the TCP dial phase via net.Dialer.DialContext, giving
// callers real cancellation rather than relying solely on the connect timeout.
func dial(ctx context.Context, cfg Config) (*ssh.Client, error) {
	_, host, port := parseAddress(cfg.Address)
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	clientCfg, err := newClientConfig(cfg)
	if err != nil {
		return nil, err
	}

	// Use DialContext so we respect ctx cancellation during the TCP handshake.
	netDialer := &net.Dialer{Timeout: dialTimeout}
	tcpConn, err := netDialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(tcpConn, addr, clientCfg)
	if err != nil {
		tcpConn.Close()
		return nil, fmt.Errorf("ssh handshake %s: %w", addr, err)
	}

	return ssh.NewClient(sshConn, chans, reqs), nil
}

// Exec runs command on the remote host and returns the combined stdout+stderr
// output. It is suitable for one-shot commands like "docker compose restart".
func Exec(ctx context.Context, cfg Config, command string) (string, error) {
	client, err := dial(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("ssh exec: %w", err)
	}
	defer client.Close()

	// Watch for context cancellation and close the client to unblock the session.
	cancelOnce := &sync.Once{}
	cancelFunc := func() { cancelOnce.Do(func() { client.Close() }) }
	go func() {
		select {
		case <-ctx.Done():
			cancelFunc()
		case <-clientDone(client):
		}
	}()

	sess, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh exec: open session: %w", err)
	}
	defer sess.Close()

	out, err := sess.CombinedOutput(command)
	if err != nil {
		// If context was cancelled, surface that error instead.
		if ctx.Err() != nil {
			return string(out), fmt.Errorf("ssh exec: %w", ctx.Err())
		}
		return string(out), fmt.Errorf("ssh exec %q: %w", command, err)
	}
	return string(out), nil
}

// Stream runs command on the remote host, copying stdout and stderr to the
// provided writers in real-time. It blocks until the command completes or ctx
// is cancelled. This is used for streaming commands like "docker logs -f".
func Stream(ctx context.Context, cfg Config, command string, stdout, stderr io.Writer) error {
	client, err := dial(ctx, cfg)
	if err != nil {
		return fmt.Errorf("ssh stream: %w", err)
	}
	defer client.Close()

	// Cancel goroutine: close the client when ctx fires so session.Wait()
	// unblocks even if the remote command is still running.
	cancelOnce := &sync.Once{}
	cancelFunc := func() { cancelOnce.Do(func() { client.Close() }) }
	go func() {
		select {
		case <-ctx.Done():
			cancelFunc()
		case <-clientDone(client):
		}
	}()

	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("ssh stream: open session: %w", err)
	}
	defer sess.Close()

	sess.Stdout = stdout
	sess.Stderr = stderr

	if err := sess.Start(command); err != nil {
		return fmt.Errorf("ssh stream: start %q: %w", command, err)
	}

	if err := sess.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil // Ctrl+C / context cancelled — clean exit
		}
		return fmt.Errorf("ssh stream %q: %w", command, err)
	}
	return nil
}

// clientDone returns a channel that closes when the SSH client's underlying
// connection is closed. We use it to release the context-watcher goroutine so
// it doesn't leak after the command finishes normally.
func clientDone(c *ssh.Client) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		// Wait() on the ssh.Client blocks until the transport closes.
		c.Wait() //nolint:errcheck
		close(ch)
	}()
	return ch
}
