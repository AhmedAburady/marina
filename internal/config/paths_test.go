package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/AhmedAburady/marina/internal/config"
)

func expectedMarinaDir(t *testing.T, home string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		base, err := os.UserConfigDir()
		if err != nil {
			t.Fatalf("os.UserConfigDir: %v", err)
		}
		return filepath.Join(base, "marina")
	}
	return filepath.Join(home, ".config", "marina")
}

func TestResolveConfigDir_CreatesAndReturnsPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	want := expectedMarinaDir(t, tmp)

	got, err := config.ResolveConfigDir()
	if err != nil {
		t.Fatalf("ResolveConfigDir: %v", err)
	}
	if got != want {
		t.Errorf("ResolveConfigDir = %q; want %q", got, want)
	}
	if _, statErr := os.Stat(got); statErr != nil {
		t.Errorf("config dir was not created: %v", statErr)
	}
}

func TestResolveConfigDirForWrite_SameAsRead(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	read, err := config.ResolveConfigDir()
	if err != nil {
		t.Fatalf("ResolveConfigDir: %v", err)
	}
	write, err := config.ResolveConfigDirForWrite()
	if err != nil {
		t.Fatalf("ResolveConfigDirForWrite: %v", err)
	}
	if read != write {
		t.Errorf("read %q != write %q; the two helpers should now return the same dir", read, write)
	}
}
