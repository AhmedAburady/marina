package registry

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const CacheTTL = 30 * time.Minute

// CacheEntry holds a single cached registry check result.
type CacheEntry struct {
	ImageRef  string       `json:"image_ref"`
	Digest    string       `json:"digest"`
	Status    UpdateStatus `json:"status"`
	CheckedAt time.Time    `json:"checked_at"`
}

// Cache is a file-backed store for registry check results.
// The mu guard covers the Entries map, which is accessed concurrently
// from goroutines launched by the TUI's parallel check loop.
type Cache struct {
	mu      sync.Mutex
	Entries map[string]CacheEntry `json:"entries"` // key: imageRef|digest
}

// DefaultCachePath returns the canonical on-disk path for the cache file.
func DefaultCachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "marina", "check-cache.json"), nil
}

// LoadCache reads the cache from path (or DefaultCachePath if path is "").
// A missing file is treated as an empty cache rather than an error.
func LoadCache(path string) (*Cache, error) {
	if path == "" {
		var err error
		path, err = DefaultCachePath()
		if err != nil {
			return nil, err
		}
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Cache{Entries: make(map[string]CacheEntry)}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		// Corrupt cache — start fresh rather than aborting.
		return &Cache{Entries: make(map[string]CacheEntry)}, nil
	}
	if c.Entries == nil {
		c.Entries = make(map[string]CacheEntry)
	}
	return &c, nil
}

// SaveCache writes the cache to path (or DefaultCachePath if path is "").
// This is called from the main goroutine after the TUI exits, so it does
// not hold the mutex — all goroutines have already finished at that point.
func SaveCache(cache *Cache, path string) error {
	if path == "" {
		var err error
		path, err = DefaultCachePath()
		if err != nil {
			return err
		}
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Lookup returns a cached UpdateStatus if the entry exists and hasn't expired.
// Expired entries are evicted in place. Thread-safe.
func (c *Cache) Lookup(imageRef, digest string) (UpdateStatus, bool) {
	key := imageRef + "|" + digest
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.Entries[key]
	if !ok {
		return 0, false
	}
	if time.Since(entry.CheckedAt) > CacheTTL {
		delete(c.Entries, key)
		return 0, false
	}
	return entry.Status, true
}

// Store saves a check result. Thread-safe.
func (c *Cache) Store(imageRef, digest string, status UpdateStatus) {
	key := imageRef + "|" + digest
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Entries[key] = CacheEntry{
		ImageRef:  imageRef,
		Digest:    digest,
		Status:    status,
		CheckedAt: time.Now(),
	}
}
