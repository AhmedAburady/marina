package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// ResolveConfigDir returns the marina config directory, creating it if it
// does not exist.
//
// On unix (linux, darwin, *bsd) we always use ~/.config/marina/ — the XDG
// convention that most developer CLIs follow. macOS users get ~/.config
// rather than ~/Library/Application Support/ because the latter is awkward
// to browse, breaks dotfiles repos, and has a space in the path.
//
// On Windows we use os.UserConfigDir() which returns %AppData%.
func ResolveConfigDir() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func configDir() (string, error) {
	if runtime.GOOS == "windows" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "marina"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "marina"), nil
}
