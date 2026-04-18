package actions

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/AhmedAburady/marina/internal/config"
	internalssh "github.com/AhmedAburady/marina/internal/ssh"
)

// HostRow is the display snapshot of a configured host. Both the cobra
// `marina hosts` table and the TUI Hosts screen render from this shape so
// column output stays consistent.
type HostRow struct {
	Name     string
	User     string // resolved effective user ("(global)" suffix when inherited)
	Address  string
	Key      string // resolved key path ("(global)" suffix when inherited)
	Disabled bool
}

// EnabledHosts returns every non-disabled host in cfg. This is the SINGLE
// canonical filter for fan-out operations — ps, stacks, check, update,
// prune, and every TUI screen that fans out. Anything that iterates hosts
// to probe/act on goes through here, so a host with Disabled=true is
// invisible to all of them. Explicit per-host targeting via -H / a picker
// selection bypasses this filter (see resolveTargets).
func EnabledHosts(cfg *config.Config) map[string]*config.HostConfig {
	out := make(map[string]*config.HostConfig, len(cfg.Hosts))
	for name, h := range cfg.Hosts {
		if h.Disabled {
			continue
		}
		out[name] = h
	}
	return out
}

// ListHosts returns every configured host as a HostRow, sorted by name.
func ListHosts(cfg *config.Config) []HostRow {
	out := make([]HostRow, 0, len(cfg.Hosts))
	for name, h := range cfg.Hosts {
		out = append(out, HostRow{
			Name:     name,
			User:     resolvedUser(h, cfg),
			Address:  h.Address,
			Key:      resolvedKey(h, cfg),
			Disabled: h.Disabled,
		})
	}
	slices.SortFunc(out, func(a, b HostRow) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// SetHostDisabled toggles the Disabled flag on a host and persists the config.
// Returns the new state, or an error if the host isn't configured.
func SetHostDisabled(cfg *config.Config, configPath, name string, disabled bool) error {
	h, ok := cfg.Hosts[name]
	if !ok {
		return fmt.Errorf("host %q not found", name)
	}
	h.Disabled = disabled
	return config.Save(cfg, configPath)
}

func resolvedUser(h *config.HostConfig, cfg *config.Config) string {
	if h.User != "" {
		return h.User
	}
	if cfg.Settings.Username != "" {
		return cfg.Settings.Username + " (global)"
	}
	return "(none)"
}

func resolvedKey(h *config.HostConfig, cfg *config.Config) string {
	if h.SSHKey != "" {
		return h.SSHKey
	}
	if cfg.Settings.SSHKey != "" {
		return cfg.Settings.SSHKey + " (global)"
	}
	return "(none)"
}

// JoinUserAddress builds the "user@host" form ParseAddress expects, or just
// "host" when user is empty. UIs that split user/host across two inputs
// (TUI forms, CLI --user + --address flags) use this so the wire format
// stays consistent across surfaces.
func JoinUserAddress(user, address string) string {
	if user == "" {
		return address
	}
	return user + "@" + address
}

// ParsedAddress is the output of ParseAddress: an optional user, a required
// remote address, and the original raw input for error messages.
type ParsedAddress struct {
	User    string
	Address string
}

// ParseAddress splits a raw address string into optional user + required
// host[:port]. Accepts `user@host`, `host`, or `ssh://user@host` forms.
// Returns an error when the resulting address is empty.
func ParseAddress(raw string) (ParsedAddress, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "ssh://")
	if raw == "" {
		return ParsedAddress{}, fmt.Errorf("address is required")
	}

	var out ParsedAddress
	if before, after, ok := strings.Cut(raw, "@"); ok {
		out.User = before
		out.Address = after
	} else {
		out.Address = raw
	}
	if out.Address == "" {
		return ParsedAddress{}, fmt.Errorf("address must not be empty (got %q)", raw)
	}
	return out, nil
}

// AddHost writes a new host entry to the config. The address argument is
// parsed by ParseAddress so callers can pass the raw user-entered string
// untouched. `sshKey` is optional.
//
// Returns an error if a host with the same name already exists.
func AddHost(cfg *config.Config, configPath, name, rawAddress, sshKey string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if _, exists := cfg.Hosts[name]; exists {
		return fmt.Errorf("host %q already exists; remove it first with: marina hosts remove %s", name, name)
	}

	parsed, err := ParseAddress(rawAddress)
	if err != nil {
		return err
	}

	cfg.Hosts[name] = &config.HostConfig{
		Address: parsed.Address,
		User:    parsed.User,
		SSHKey:  sshKey,
	}
	return config.Save(cfg, configPath)
}

// UpdateHost applies edits to an existing host entry. The address is parsed
// with ParseAddress so callers can pass the raw user-entered string. All
// fields are overwritten with the provided values — pass the current value
// unchanged to leave a field alone.
//
// Returns an error if the host does not exist or the address fails to parse.
func UpdateHost(cfg *config.Config, configPath, name, rawAddress, sshKey string, disabled bool) error {
	h, ok := cfg.Hosts[name]
	if !ok {
		return fmt.Errorf("host %q not found", name)
	}
	parsed, err := ParseAddress(rawAddress)
	if err != nil {
		return err
	}
	h.Address = parsed.Address
	h.User = parsed.User
	h.SSHKey = sshKey
	h.Disabled = disabled
	return config.Save(cfg, configPath)
}

// RemoveHosts deletes one or more hosts from the config. Returns the names
// that were actually removed and the names that weren't found.
func RemoveHosts(cfg *config.Config, configPath string, names ...string) (removed, notFound []string, err error) {
	for _, n := range names {
		if _, ok := cfg.Hosts[n]; !ok {
			notFound = append(notFound, n)
			continue
		}
		delete(cfg.Hosts, n)
		removed = append(removed, n)
	}
	if len(removed) > 0 {
		if err = config.Save(cfg, configPath); err != nil {
			return
		}
	}
	return
}

// TestResult is one TestHost outcome. OK true means the SSH round-trip
// succeeded; err is set on failure. Untrusted specifically marks failures
// caused by the host's key not being in ~/.ssh/known_hosts — see TrustHost
// for the corresponding fix action.
type TestResult struct {
	Host      string
	OK        bool
	Latency   time.Duration
	Err       error
	Untrusted bool
}

// TestHost runs a synchronous `echo ok` probe against one host using the
// resolved SSH config. Bound by the caller's context so a slow host can't
// block ^C. When the probe fails because the host key isn't trusted,
// TestResult.Untrusted is set so UIs can offer a "trust this host" action
// instead of a generic "unreachable" error.
func TestHost(ctx context.Context, cfg *config.Config, name string) TestResult {
	h, ok := cfg.Hosts[name]
	if !ok {
		return TestResult{Host: name, Err: fmt.Errorf("host %q not found", name)}
	}
	sshCfg := internalssh.Config{
		Address: h.SSHAddress(cfg.Settings.Username),
		KeyPath: h.ResolvedSSHKey(cfg.Settings.SSHKey),
	}
	start := time.Now()
	output, err := internalssh.Exec(ctx, sshCfg, "echo ok")
	result := TestResult{
		Host:    name,
		OK:      err == nil,
		Latency: time.Since(start),
		Err:     err,
	}
	if err != nil && strings.Contains(output, "Host key verification failed") {
		result.Untrusted = true
	}
	return result
}

// IsHostTrusted returns true if the host already has an entry in the
// user's ~/.ssh/known_hosts. Used to decide whether to prompt for trust
// (TUI/CLI add flow) — a trusted host just skips straight to testing.
func IsHostTrusted(ctx context.Context, cfg *config.Config, name string) (bool, error) {
	h, ok := cfg.Hosts[name]
	if !ok {
		return false, fmt.Errorf("host %q not found", name)
	}
	addr, port := splitHostPort(h.Address)
	if addr == "" {
		return false, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	knownHosts := filepath.Join(home, ".ssh", "known_hosts")
	out, _ := exec.CommandContext(ctx, "ssh-keygen", "-F", knownHostsLookup(addr, port), "-f", knownHosts).Output()
	return len(out) > 0, nil
}

// splitHostPort splits "host" or "host:port" into its parts. Returns empty
// strings when the input is empty; no error path because callers already
// validated the address at add time.
func splitHostPort(addr string) (host, port string) {
	host = addr
	if i := strings.LastIndex(addr, ":"); i >= 0 && !strings.Contains(addr, "]") {
		host, port = addr[:i], addr[i+1:]
	}
	return host, port
}

// knownHostsLookup returns the string that ssh-keygen -F expects to match
// an entry — bare host for port 22, "[host]:port" otherwise.
func knownHostsLookup(host, port string) string {
	if port == "" || port == "22" {
		return host
	}
	return fmt.Sprintf("[%s]:%s", host, port)
}

// TrustHost appends the host's public key to ~/.ssh/known_hosts via
// ssh-keyscan — the non-interactive equivalent of typing "yes" at the
// first-connection prompt. Refuses if the host already has a key entry,
// leaving key-change (possible MitM) cases for the user to resolve with
// ssh-keygen -R.
func TrustHost(ctx context.Context, cfg *config.Config, name string) error {
	h, ok := cfg.Hosts[name]
	if !ok {
		return fmt.Errorf("host %q not found", name)
	}

	addr, port := splitHostPort(h.Address)
	if addr == "" {
		return fmt.Errorf("host %q has no address", name)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	knownHosts := filepath.Join(home, ".ssh", "known_hosts")

	// Refuse to trust if the host already has a key registered: a mismatch
	// here is the "REMOTE HOST IDENTIFICATION HAS CHANGED" scenario and is
	// the user's problem to resolve manually.
	lookup := knownHostsLookup(addr, port)
	if out, _ := exec.CommandContext(ctx, "ssh-keygen", "-F", lookup, "-f", knownHosts).Output(); len(out) > 0 {
		return fmt.Errorf("%s already has a key in known_hosts — remove it first with: ssh-keygen -R %s", lookup, lookup)
	}

	// Grab the host key. -H hashes the hostname entry for privacy; -T caps
	// the probe at 5s so a dead host doesn't hang the UI.
	scanArgs := []string{"-T", "5", "-H"}
	if port != "" {
		scanArgs = append(scanArgs, "-p", port)
	}
	scanArgs = append(scanArgs, addr)
	scanOut, err := exec.CommandContext(ctx, "ssh-keyscan", scanArgs...).Output()
	if err != nil {
		return fmt.Errorf("ssh-keyscan %s: %w", addr, err)
	}
	if len(strings.TrimSpace(string(scanOut))) == 0 {
		return fmt.Errorf("ssh-keyscan returned no keys for %s", addr)
	}

	if err := os.MkdirAll(filepath.Dir(knownHosts), 0o700); err != nil {
		return fmt.Errorf("create .ssh dir: %w", err)
	}
	f, err := os.OpenFile(knownHosts, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open known_hosts: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(scanOut); err != nil {
		return fmt.Errorf("append known_hosts: %w", err)
	}
	return nil
}

// HostSSHConfig resolves the ssh.Config for a given host entry, applying
// global defaults. Returns nil when the host isn't configured.
func HostSSHConfig(cfg *config.Config, name string) *internalssh.Config {
	h, ok := cfg.Hosts[name]
	if !ok {
		return nil
	}
	return &internalssh.Config{
		Address: h.SSHAddress(cfg.Settings.Username),
		KeyPath: h.ResolvedSSHKey(cfg.Settings.SSHKey),
	}
}
