package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// DefaultPath returns ~/.config/marina/state.json
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "marina", "state.json"), nil
}

// HostSnapshot holds the last-known container state for a single host.
type HostSnapshot struct {
	Containers []ContainerState `json:"containers"`
	UpdatedAt  time.Time        `json:"updated_at"`
}

// ContainerState is a minimal representation of a container for caching.
type ContainerState struct {
	ID     string            `json:"id"`
	Names  []string          `json:"names"`
	Image  string            `json:"image"`
	State  string            `json:"state"`
	Status string            `json:"status"`
	Labels map[string]string `json:"labels"`
	Ports  []PortState       `json:"ports,omitempty"`
}

// PortState is a minimal port mapping for caching.
type PortState struct {
	PublicPort  uint16 `json:"public_port"`
	PrivatePort uint16 `json:"private_port"`
	Type        string `json:"type"`
}

// Store holds state for all hosts.
type Store struct {
	Hosts map[string]*HostSnapshot `json:"hosts"`
}

// Load reads the state file. Returns an empty Store if the file doesn't exist.
func Load(path string) (*Store, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Store{Hosts: make(map[string]*HostSnapshot)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}

	var store Store
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}
	if store.Hosts == nil {
		store.Hosts = make(map[string]*HostSnapshot)
	}
	return &store, nil
}

// Save writes the store to the state file.
func Save(store *Store, path string) error {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return err
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write state %s: %w", path, err)
	}
	return nil
}

// SaveHostSnapshot updates the snapshot for a single host and persists.
func SaveHostSnapshot(hostName string, snapshot *HostSnapshot, path string) error {
	store, err := Load(path)
	if err != nil {
		// Non-fatal — start fresh if state is corrupted.
		store = &Store{Hosts: make(map[string]*HostSnapshot)}
	}
	snapshot.UpdatedAt = time.Now()
	store.Hosts[hostName] = snapshot
	return Save(store, path)
}
