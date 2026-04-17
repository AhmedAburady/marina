package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/AhmedAburady/marina/internal/config"
)

// canonicalMarinaDir returns the expected canonical marina config directory
// for a given HOME (and XDG_CONFIG_HOME if set). It calls os.UserConfigDir()
// so the result is platform-correct: ~/Library/Application Support on macOS,
// $XDG_CONFIG_HOME or ~/.config on Linux, %AppData% on Windows.
func canonicalMarinaDir(t *testing.T) string {
	t.Helper()
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("os.UserConfigDir: %v", err)
	}
	return filepath.Join(base, "marina")
}

// TestResolveConfigDir_LegacyFallback verifies that ResolveConfigDir returns
// the legacy ~/.config/marina/ path when the canonical dir does not exist but
// the legacy one does. We simulate this by redirecting HOME to a temp dir and
// creating only the legacy subtree.
func TestResolveConfigDir_LegacyFallback(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Create the legacy directory but NOT the canonical one.
	legacy := filepath.Join(tmp, ".config", "marina")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatalf("create legacy dir: %v", err)
	}

	// Only applicable when canonical != legacy (i.e. macOS or a custom
	// XDG_CONFIG_HOME). If they happen to be the same path (e.g. Linux with
	// XDG_CONFIG_HOME=~/.config), creating the legacy dir is the same as
	// creating the canonical dir, so the fallback branch is never hit — skip.
	canonical := canonicalMarinaDir(t)
	if canonical == legacy {
		t.Skip("canonical and legacy dirs are the same; fallback not testable on this config")
	}

	got, err := config.ResolveConfigDir()
	if err != nil {
		t.Fatalf("ResolveConfigDir: %v", err)
	}
	if got != legacy {
		t.Errorf("ResolveConfigDir = %q; want legacy %q", got, legacy)
	}
}

// TestResolveConfigDirForWrite_AlwaysCanonical verifies that
// ResolveConfigDirForWrite always returns and creates the canonical dir, even
// when the legacy dir exists. This ensures writes go to the new location.
func TestResolveConfigDirForWrite_AlwaysCanonical(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	canonical := canonicalMarinaDir(t)

	// Create only the legacy directory (only meaningful when they differ).
	legacy := filepath.Join(tmp, ".config", "marina")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatalf("create legacy dir: %v", err)
	}

	got, err := config.ResolveConfigDirForWrite()
	if err != nil {
		t.Fatalf("ResolveConfigDirForWrite: %v", err)
	}
	if got != canonical {
		t.Errorf("ResolveConfigDirForWrite = %q; want canonical %q", got, canonical)
	}
	if _, statErr := os.Stat(canonical); statErr != nil {
		t.Errorf("canonical dir was not created: %v", statErr)
	}
}

// TestResolveConfigDir_FirstRun verifies that when neither canonical nor
// legacy dirs exist, ResolveConfigDir creates and returns the canonical dir.
func TestResolveConfigDir_FirstRun(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	canonical := canonicalMarinaDir(t)

	got, err := config.ResolveConfigDir()
	if err != nil {
		t.Fatalf("ResolveConfigDir: %v", err)
	}
	if got != canonical {
		t.Errorf("ResolveConfigDir = %q; want canonical %q", got, canonical)
	}
	if _, statErr := os.Stat(canonical); statErr != nil {
		t.Errorf("canonical dir was not created: %v", statErr)
	}
}
