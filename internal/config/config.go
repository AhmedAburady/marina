// Package config handles loading and saving marina's YAML configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

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
	URL      string `yaml:"url"`
	Token    string `yaml:"token"`
	Priority int    `yaml:"priority"`
}

// DefaultPath returns the default config file path: ~/.config/marina/config.yaml
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "marina", "config.yaml"), nil
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
	return &cfg, nil
}

// Save writes cfg to path. If path is empty, DefaultPath is used.
// Parent directories are created as needed.
func Save(cfg *Config, path string) error {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return err
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
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
