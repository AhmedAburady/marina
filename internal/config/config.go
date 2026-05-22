// Package config handles loading and saving marina's YAML configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	internalssh "github.com/AhmedAburady/marina/internal/ssh"
	"go.yaml.in/yaml/v3"
)

// Config is the top-level configuration structure.
type Config struct {
	Hosts    map[string]*HostConfig `yaml:"hosts"`
	Settings Settings               `yaml:"settings"`
	Notify   NotifyConfig           `yaml:"notifications"`
}

// SSH authentication methods, used by HostConfig.AuthMethod and
// Settings.AuthMethod. An empty value means "inherit / infer".
const (
	AuthMethodKey   = "key"   // authenticate with a private key file (-i)
	AuthMethodAgent = "agent" // authenticate via the SSH agent (e.g. 1Password)
)

// HostConfig represents a single remote Docker host.
type HostConfig struct {
	// Address is just the IP or hostname, e.g. "10.0.0.50" or "synology.tail"
	Address string `yaml:"address"`
	// User is an optional per-host SSH user override. When empty, the global
	// Settings.Username is used instead.
	User string `yaml:"user,omitempty"`
	// SSHKey is an optional per-host SSH key path override. When empty, the
	// global Settings.SSHKey is used instead.
	SSHKey string `yaml:"ssh_key,omitempty"`
	// AuthMethod selects how this host authenticates: AuthMethodKey or
	// AuthMethodAgent. Empty inherits Settings.AuthMethod, and failing that is
	// inferred from whether an SSH key resolves (key when one does, else agent).
	AuthMethod string `yaml:"auth_method,omitempty"`
	// SSHAgentSocket optionally pins the SSH agent socket (e.g. 1Password) used
	// in agent mode, overriding $SSH_AUTH_SOCK. Empty falls back to the global
	// Settings.SSHAgentSocket, and an empty resolved value means $SSH_AUTH_SOCK.
	SSHAgentSocket string `yaml:"ssh_agent_socket,omitempty"`
	// Stacks maps stack name → compose project directory on the remote host.
	// Used as a fallback for stacks that are fully stopped (no running containers).
	Stacks map[string]string `yaml:"stacks,omitempty"`
	// Disabled skips this host from every operation that fans out across all
	// hosts (ps, stacks, check, update, TUI dashboards). It still appears in
	// `marina hosts` so the user can re-enable it.
	Disabled bool `yaml:"disabled,omitempty"`
}

// ResolvedSSHKey returns the SSH key path for this host, falling back to
// the global key when no per-host override is set.
func (h *HostConfig) ResolvedSSHKey(globalKey string) string {
	if h.SSHKey != "" {
		return h.SSHKey
	}
	return globalKey
}

// ResolvedAuthMethod returns the effective auth method for this host:
// the per-host override, else the global default, else inferred from whether
// an SSH key resolves (AuthMethodKey when one does, otherwise AuthMethodAgent).
// The inference preserves marina's prior behaviour: a configured key means key
// auth, while a keyless host falls through to the agent ($SSH_AUTH_SOCK).
func (h *HostConfig) ResolvedAuthMethod(s Settings) string {
	if h.AuthMethod != "" {
		return h.AuthMethod
	}
	if s.AuthMethod != "" {
		return s.AuthMethod
	}
	if h.ResolvedSSHKey(s.SSHKey) != "" {
		return AuthMethodKey
	}
	return AuthMethodAgent
}

// ResolvedAgentSocket returns the agent socket for this host, falling back to
// the global socket. An empty result means "use $SSH_AUTH_SOCK".
func (h *HostConfig) ResolvedAgentSocket(globalSocket string) string {
	if h.SSHAgentSocket != "" {
		return h.SSHAgentSocket
	}
	return globalSocket
}

// SSHConfig resolves the transport-level ssh.Config for this host, applying the
// chosen auth method and global Settings fallbacks. This is the single place
// that turns config into the credentials handed to the ssh binary.
func (h *HostConfig) SSHConfig(s Settings) internalssh.Config {
	c := internalssh.Config{Address: h.SSHAddress(s.Username)}
	switch h.ResolvedAuthMethod(s) {
	case AuthMethodAgent:
		c.UseAgent = true
		c.AgentSocket = h.ResolvedAgentSocket(s.SSHAgentSocket)
	default: // AuthMethodKey
		c.KeyPath = h.ResolvedSSHKey(s.SSHKey)
	}
	return c
}

// SSHAddress returns the full SSH URL for this host, using the provided
// fallback username when the host has no per-host User set.
func (h *HostConfig) SSHAddress(globalUser string) string {
	user := h.User
	if user == "" {
		user = globalUser
	}
	if user != "" {
		return "ssh://" + user + "@" + h.Address
	}
	return "ssh://" + h.Address
}

// Settings holds global marina settings.
type Settings struct {
	Username string `yaml:"username"`
	SSHKey   string `yaml:"ssh_key"`
	// AuthMethod is the global default auth method (AuthMethodKey or
	// AuthMethodAgent) for hosts that don't set their own. Empty infers from
	// whether ssh_key is set.
	AuthMethod string `yaml:"auth_method,omitempty"`
	// SSHAgentSocket is the global default agent socket for agent mode.
	SSHAgentSocket   string `yaml:"ssh_agent_socket,omitempty"`
	PruneAfterUpdate bool   `yaml:"prune_after_update"`
}

// NotifyConfig holds notification backend configuration.
type NotifyConfig struct {
	Gotify GotifyConfig `yaml:"gotify"`
}

// GotifyConfig holds Gotify push notification settings.
type GotifyConfig struct {
	URL string `yaml:"url"`
	// Token is the Gotify app token in plaintext. Prefer TokenEnv to keep the
	// secret out of the config file (see token_env). Keep config.yaml at 0600.
	Token string `yaml:"token"`
	// TokenEnv names an environment variable that holds the Gotify app token.
	// When set and Token is empty, the token is read from os.Getenv(TokenEnv).
	// Example: token_env: MARINA_GOTIFY_TOKEN
	TokenEnv string `yaml:"token_env,omitempty"`
	Priority int    `yaml:"priority"`
}

// DefaultPath returns the default config file path:
//   - unix (linux, darwin, *bsd): ~/.config/marina/config.yaml
//   - windows:                    %AppData%\marina\config.yaml
//
// XDG_CONFIG_HOME is intentionally not honoured on unix; see paths.go for
// the rationale on keeping ~/.config on all unix (including macOS).
func DefaultPath() (string, error) {
	dir, err := ResolveConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config dir: %w", err)
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// Load reads the config file at path. If path is empty, DefaultPath is used.
// If the file does not exist, a fresh empty Config is returned (not an error).
func Load(path string) (*Config, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{Hosts: make(map[string]*HostConfig)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.Hosts == nil {
		cfg.Hosts = make(map[string]*HostConfig)
	}

	// Expand tilde and environment variables in SSH key paths so that the
	// config example (ssh_key: ~/.ssh/id_ed25519) works out of the box.
	cfg.Settings.SSHKey = expandPath(cfg.Settings.SSHKey)
	cfg.Settings.SSHAgentSocket = expandPath(cfg.Settings.SSHAgentSocket)
	for _, h := range cfg.Hosts {
		h.SSHKey = expandPath(h.SSHKey)
		h.SSHAgentSocket = expandPath(h.SSHAgentSocket)
	}

	return &cfg, nil
}

// Save writes cfg to path atomically. If path is empty, the default config
// path is used. Parent directories are created as needed.
func Save(cfg *Config, path string) error {
	if path == "" {
		dir, err := ResolveConfigDir()
		if err != nil {
			return fmt.Errorf("resolve config dir: %w", err)
		}
		path = filepath.Join(dir, "config.yaml")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// If path is a symlink (e.g. managed from a dotfiles repo), resolve it so
	// the atomic rename swaps the underlying file rather than replacing the
	// symlink with a regular file.
	writePath := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		writePath = resolved
		dir = filepath.Dir(resolved)
	}

	// Contract SSH key paths before writing so the file stays portable
	// (~/... form) even though the in-memory config holds expanded paths.
	toSave := *cfg
	toSave.Settings.SSHKey = ContractPath(cfg.Settings.SSHKey)
	toSave.Settings.SSHAgentSocket = ContractPath(cfg.Settings.SSHAgentSocket)
	hostsCopy := make(map[string]*HostConfig, len(cfg.Hosts))
	for k, v := range cfg.Hosts {
		hc := *v
		hc.SSHKey = ContractPath(v.SSHKey)
		hc.SSHAgentSocket = ContractPath(v.SSHAgentSocket)
		hostsCopy[k] = &hc
	}
	toSave.Hosts = hostsCopy

	data, err := yaml.Marshal(&toSave)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.tmp.yaml")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	tmpName := tmp.Name()

	var writeOK bool
	defer func() {
		if !writeOK {
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp config %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp config %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, writePath); err != nil {
		return fmt.Errorf("rename temp config to %s: %w", writePath, err)
	}
	writeOK = true
	return nil
}

// ContractPath replaces the user's home directory prefix with "~/".
// It is the symmetric inverse of expandPath and is used when writing config
// files so that SSH key paths stay portable (~/... form) rather than
// hard-coding the current user's home directory.
func ContractPath(p string) string {
	if p == "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~/" + p[len(home)+1:]
	}
	return p
}

// expandPath expands environment variables and a leading tilde in p.
// Returns p unchanged if p is empty. The tilde is resolved via
// os.UserHomeDir so it works even when $HOME is unset.
func expandPath(p string) string {
	if p == "" {
		return p
	}
	p = os.ExpandEnv(p)
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			p = filepath.Join(home, p[2:])
		}
	} else if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			p = home
		}
	}
	return p
}

// Validate checks the config for obvious errors.
func Validate(cfg *Config) []string {
	var errs []string
	if !validAuthMethod(cfg.Settings.AuthMethod) {
		errs = append(errs, fmt.Sprintf("settings: auth_method must be %q, %q, or empty (to infer from ssh_key)", AuthMethodKey, AuthMethodAgent))
	}
	// With no hosts to exercise it, a key-mode settings default with no key
	// slips past the per-host check below (which never runs). Catch it upfront
	// so saving such a config warns immediately, before the first host exists.
	if len(cfg.Hosts) == 0 && cfg.Settings.AuthMethod == AuthMethodKey && cfg.Settings.SSHKey == "" {
		errs = append(errs, fmt.Sprintf("settings: auth_method is %q but no ssh_key is set", AuthMethodKey))
	}
	for name, h := range cfg.Hosts {
		if h.Address == "" {
			errs = append(errs, fmt.Sprintf("host %q: address is required", name))
		}
		if !validAuthMethod(h.AuthMethod) {
			errs = append(errs, fmt.Sprintf("host %q: auth_method must be %q, %q, or empty (to infer from ssh_key)", name, AuthMethodKey, AuthMethodAgent))
			continue
		}
		// Key mode needs a key to point -i at; agent mode is fine without a
		// socket (it falls back to $SSH_AUTH_SOCK).
		if h.ResolvedAuthMethod(cfg.Settings) == AuthMethodKey && h.ResolvedSSHKey(cfg.Settings.SSHKey) == "" {
			errs = append(errs, fmt.Sprintf("host %q: auth_method is %q but no ssh_key is set (here or in settings)", name, AuthMethodKey))
		}
	}
	return errs
}

// validAuthMethod reports whether m is an accepted auth_method value. Empty is
// valid and means "inherit / infer".
func validAuthMethod(m string) bool {
	return m == "" || m == AuthMethodKey || m == AuthMethodAgent
}
