package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/AhmedAburady/marina/internal/config"
)

// TestLoadSaveRoundTrip loads a config, saves it to a new path, reloads it,
// and asserts deep equality. SSH key paths use already-absolute values so the
// expandPath pass is idempotent across both Load calls.
func TestLoadSaveRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")

	original := &config.Config{
		Hosts: map[string]*config.HostConfig{
			"prod": {
				Address: "10.0.0.1",
				User:    "admin",
				SSHKey:  "/tmp/id_ed25519",
				Stacks:  map[string]string{"webapp": "/opt/webapp"},
			},
			"staging": {
				Address: "10.0.0.2",
			},
		},
		Settings: config.Settings{
			Username:         "ops",
			SSHKey:           "/tmp/default_key",
			PruneAfterUpdate: true,
		},
		Notify: config.NotifyConfig{
			Gotify: config.GotifyConfig{
				URL:      "https://gotify.example.com",
				Token:    "tok123",
				Priority: 5,
			},
		},
	}

	if err := config.Save(original, path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !reflect.DeepEqual(original, loaded) {
		t.Errorf("round-trip mismatch\ngot:  %+v\nwant: %+v", loaded, original)
	}
}

// TestTildeExpansionSettings confirms that "~/..." in Settings.SSHKey is
// expanded to an absolute path containing the home directory during Load.
func TestTildeExpansionSettings(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")

	raw := `
settings:
  ssh_key: "~/.ssh/id_ed25519"
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := filepath.Join(fakeHome, ".ssh", "id_ed25519")
	if cfg.Settings.SSHKey != want {
		t.Errorf("Settings.SSHKey = %q; want %q", cfg.Settings.SSHKey, want)
	}
}

// TestTildeExpansionHostOverride confirms that per-host ssh_key tilde expansion
// works independently of the global settings expansion.
func TestTildeExpansionHostOverride(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")

	raw := `
hosts:
  myhost:
    address: "192.168.1.10"
    ssh_key: "~/.ssh/host_key"
settings:
  ssh_key: "~/.ssh/default_key"
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	wantHost := filepath.Join(fakeHome, ".ssh", "host_key")
	if cfg.Hosts["myhost"].SSHKey != wantHost {
		t.Errorf("host SSHKey = %q; want %q", cfg.Hosts["myhost"].SSHKey, wantHost)
	}

	wantGlobal := filepath.Join(fakeHome, ".ssh", "default_key")
	if cfg.Settings.SSHKey != wantGlobal {
		t.Errorf("Settings.SSHKey = %q; want %q", cfg.Settings.SSHKey, wantGlobal)
	}
}

// TestEnvVarExpansion confirms that $HOME/... in ssh_key is expanded via
// os.ExpandEnv during Load.
func TestEnvVarExpansion(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")

	raw := `
settings:
  ssh_key: "$HOME/keys/id"
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := filepath.Join(fakeHome, "keys", "id")
	if cfg.Settings.SSHKey != want {
		t.Errorf("Settings.SSHKey = %q; want %q", cfg.Settings.SSHKey, want)
	}
}

// TestPerHostOverridePrecedence confirms that a per-host user/ssh_key beats the
// global settings fallback via HostConfig.ResolvedSSHKey.
func TestPerHostOverridePrecedence(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")

	raw := `
hosts:
  override:
    address: "10.0.0.5"
    user: "host-user"
    ssh_key: "/host/key"
  fallback:
    address: "10.0.0.6"
settings:
  username: "global-user"
  ssh_key: "/global/key"
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Host with explicit overrides should use its own values.
	overrideHost := cfg.Hosts["override"]
	if overrideHost.User != "host-user" {
		t.Errorf("override.User = %q; want %q", overrideHost.User, "host-user")
	}
	if got := overrideHost.ResolvedSSHKey(cfg.Settings.SSHKey); got != "/host/key" {
		t.Errorf("override.ResolvedSSHKey = %q; want %q", got, "/host/key")
	}

	// Host without overrides should fall through to global settings.
	fallbackHost := cfg.Hosts["fallback"]
	if fallbackHost.User != "" {
		t.Errorf("fallback.User = %q; want empty (inherits global)", fallbackHost.User)
	}
	if got := fallbackHost.ResolvedSSHKey(cfg.Settings.SSHKey); got != "/global/key" {
		t.Errorf("fallback.ResolvedSSHKey = %q; want %q", got, "/global/key")
	}
}

// TestSaveFileMode verifies that Save writes the config file with mode 0o600.
// Save uses os.Chmod on the temp file before rename, so the final path must
// also reflect 0o600.
func TestSaveFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}

	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")

	cfg := &config.Config{Hosts: make(map[string]*config.HostConfig)}
	if err := config.Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %04o; want 0600", perm)
	}
}

// TestMissingConfigFile verifies that Load on a non-existent path returns a
// zero-value Config with an initialised Hosts map and no error. This is the
// "first run" contract.
func TestMissingConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.yaml")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load on missing file returned error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load returned nil Config")
	}
	if cfg.Hosts == nil {
		t.Error("Config.Hosts is nil; expected initialised empty map")
	}
	if len(cfg.Hosts) != 0 {
		t.Errorf("Config.Hosts has %d entries; want 0", len(cfg.Hosts))
	}
}

// TestMalformedYAML verifies that Load on syntactically invalid YAML returns a
// non-nil error and does not panic.
func TestMalformedYAML(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "bad.yaml")

	bad := []byte("hosts:\n  - : bad: [unclosed\n")
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load on malformed YAML returned nil error; expected non-nil")
	}
	if !strings.Contains(err.Error(), "parse config") {
		t.Errorf("error %q does not contain 'parse config'", err.Error())
	}
}
