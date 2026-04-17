package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// saveMu guards the Load→modify→Save sequence in SaveHostSnapshot so that
// single-host CLI callers (which do not go through FetchAllHosts' aggregation
// path) cannot race with each other.
var saveMu sync.Mutex

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

// Save writes the store to the state file atomically.
// It writes to a temporary file in the same directory, syncs, closes, chmods,
// then renames over the destination — POSIX-atomic and safe on Windows too.
func Save(store *Store, path string) error {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return err
		}
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".state-*.tmp.json")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmpName := tmp.Name()

	// Ensure the temp file is removed if anything goes wrong after creation.
	var writeOK bool
	defer func() {
		if !writeOK {
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp state %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp state %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp state %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp state %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp state to %s: %w", path, err)
	}
	writeOK = true
	return nil
}

// SaveHostSnapshot updates the snapshot for a single host and persists.
// It is guarded by a mutex so single-host callers are safe from concurrent
// Load→modify→Save races. For bulk updates across many hosts, prefer the
// aggregation pattern in FetchAllHosts (load once, merge all, save once).
func SaveHostSnapshot(hostName string, snapshot *HostSnapshot, path string) error {
	saveMu.Lock()
	defer saveMu.Unlock()

	store, err := Load(path)
	if err != nil {
		// Non-fatal — start fresh if state is corrupted.
		store = &Store{Hosts: make(map[string]*HostSnapshot)}
	}
	snapshot.UpdatedAt = time.Now()
	store.Hosts[hostName] = snapshot
	return Save(store, path)
}
