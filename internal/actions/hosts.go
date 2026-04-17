package actions

import (
	"cmp"
	"context"
	"fmt"
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
	Name    string
	User    string // resolved effective user ("(global)" suffix when inherited)
	Address string
	Key     string // resolved key path ("(global)" suffix when inherited)
}

// ListHosts returns every configured host as a HostRow, sorted by name.
func ListHosts(cfg *config.Config) []HostRow {
	out := make([]HostRow, 0, len(cfg.Hosts))
	for name, h := range cfg.Hosts {
		out = append(out, HostRow{
			Name:    name,
			User:    resolvedUser(h, cfg),
			Address: h.Address,
			Key:     resolvedKey(h, cfg),
		})
	}
	slices.SortFunc(out, func(a, b HostRow) int { return cmp.Compare(a.Name, b.Name) })
	return out
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
// succeeded; err is set on failure.
type TestResult struct {
	Host    string
	OK      bool
	Latency time.Duration
	Err     error
}

// TestHost runs a synchronous `echo ok` probe against one host using the
// resolved SSH config. Bound by the caller's context so a slow host can't
// block ^C.
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
	_, err := internalssh.Exec(ctx, sshCfg, "echo ok")
	return TestResult{
		Host:    name,
		OK:      err == nil,
		Latency: time.Since(start),
		Err:     err,
	}
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
