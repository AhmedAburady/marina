// Package config handles loading and saving marina's YAML configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Config is the top-level configuration structure.
type Config struct {
	Hosts    map[string]*HostConfig `yaml:"hosts"`
	Settings Settings               `yaml:"settings"`
	Notify   NotifyConfig           `yaml:"notifications"`
}

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
	// Stacks maps stack name → compose project directory on the remote host.
	// Used as a fallback for stacks that are fully stopped (no running containers).
	Stacks map[string]string `yaml:"stacks,omitempty"`
}

// ResolvedSSHKey returns the SSH key path for this host, falling back to
// the global key when no per-host override is set.
func (h *HostConfig) ResolvedSSHKey(globalKey string) string {
	if h.SSHKey != "" {
		return h.SSHKey
	}
	return globalKey
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
	Username         string `yaml:"username"`
	SSHKey           string `yaml:"ssh_key"`
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

// DefaultPath returns the default config file path.
// On macOS this is ~/Library/Application Support/marina/config.yaml,
// on Linux it honours XDG_CONFIG_HOME (defaulting to ~/.config/marina/),
// and on Windows it uses %AppData%\marina\config.yaml.
// A one-release read fallback returns the legacy ~/.config/marina/ path
// when the canonical directory does not yet exist but the legacy one does.
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
	for _, h := range cfg.Hosts {
		h.SSHKey = expandPath(h.SSHKey)
	}

	return &cfg, nil
}

// Save writes cfg to path atomically. If path is empty, the canonical config
// path is used (writes always go to the canonical location, never legacy).
// Parent directories are created as needed.
func Save(cfg *Config, path string) error {
	if path == "" {
		dir, err := ResolveConfigDirForWrite()
		if err != nil {
			return fmt.Errorf("resolve config dir: %w", err)
		}
		path = filepath.Join(dir, "config.yaml")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := yaml.Marshal(cfg)
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
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp config to %s: %w", path, err)
	}
	writeOK = true
	return nil
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
	for name, h := range cfg.Hosts {
		if h.Address == "" {
			errs = append(errs, fmt.Sprintf("host %q: address is required", name))
		}
	}
	return errs
}
