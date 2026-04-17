package config

import (
	"log/slog"
	"os"
	"path/filepath"
)

// ResolveConfigDir returns the marina config directory, creating it if it
// does not exist. It uses os.UserConfigDir() (XDG_CONFIG_HOME on Linux,
// ~/Library/Application Support on macOS, %AppData% on Windows).
//
// Read fallback (one-release migration window): if the canonical directory
// does not exist but the legacy ~/.config/marina/ does, the legacy path is
// returned and a migration hint is logged via slog. Writes always go to the
// canonical path regardless.
//
// If os.UserConfigDir() itself fails (e.g. $HOME unset), we fall back to
// ~/.config/marina/ via os.UserHomeDir() as a last resort.
func ResolveConfigDir() (string, error) {
	canonical, legacyDir, err := configDirCandidates()
	if err != nil {
		return "", err
	}

	// If canonical already exists (or we can create it), use it.
	if _, statErr := os.Stat(canonical); statErr == nil {
		return canonical, nil
	}

	// Canonical dir absent — check for the legacy path before creating.
	if _, statErr := os.Stat(legacyDir); statErr == nil {
		slog.Warn("marina config found in legacy location; consider moving it to the new path",
			"legacy", legacyDir, "new", canonical)
		return legacyDir, nil
	}

	// Neither exists — create the canonical directory for first-run.
	if err := os.MkdirAll(canonical, 0o700); err != nil {
		return "", err
	}
	return canonical, nil
}

// ResolveConfigDirForWrite always returns the canonical (new) config dir,
// creating it if needed. Use this for write paths so new data always lands
// in the canonical location even when legacy dir exists.
func ResolveConfigDirForWrite() (string, error) {
	canonical, _, err := configDirCandidates()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(canonical, 0o700); err != nil {
		return "", err
	}
	return canonical, nil
}

// configDirCandidates returns (canonical, legacy, error). The canonical path
// uses os.UserConfigDir(); the legacy path uses ~/.config/marina/.
func configDirCandidates() (canonical, legacy string, err error) {
	base, xdgErr := os.UserConfigDir()
	if xdgErr != nil {
		// Last-resort fallback when $HOME is unset or platform lookup fails.
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", "", homeErr
		}
		base = filepath.Join(home, ".config")
	}
	canonical = filepath.Join(base, "marina")

	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		// If we can't get home, legacy fallback is same as canonical.
		legacy = canonical
	} else {
		legacy = filepath.Join(home, ".config", "marina")
	}
	return canonical, legacy, nil
}
