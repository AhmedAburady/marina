package config

import "testing"

func TestResolvedAuthMethod(t *testing.T) {
	tests := []struct {
		name string
		host HostConfig
		set  Settings
		want string
	}{
		{"host override beats global", HostConfig{AuthMethod: AuthMethodAgent}, Settings{AuthMethod: AuthMethodKey, SSHKey: "/k"}, AuthMethodAgent},
		{"global default when host empty", HostConfig{}, Settings{AuthMethod: AuthMethodAgent}, AuthMethodAgent},
		{"infer key when a host key resolves", HostConfig{SSHKey: "/host/key"}, Settings{}, AuthMethodKey},
		{"infer agent when no key anywhere", HostConfig{}, Settings{}, AuthMethodAgent},
		{"global key cascades to a keyless host", HostConfig{}, Settings{SSHKey: "/global"}, AuthMethodKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.host.ResolvedAuthMethod(tt.set); got != tt.want {
				t.Errorf("ResolvedAuthMethod() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolvedAgentSocket(t *testing.T) {
	if got := (&HostConfig{SSHAgentSocket: "/host.sock"}).ResolvedAgentSocket("/global.sock"); got != "/host.sock" {
		t.Errorf("host override = %q, want /host.sock", got)
	}
	if got := (&HostConfig{}).ResolvedAgentSocket("/global.sock"); got != "/global.sock" {
		t.Errorf("global fallback = %q, want /global.sock", got)
	}
}

func TestSSHConfig(t *testing.T) {
	// Agent mode: pins the socket, carries no key file.
	agent := (&HostConfig{Address: "10.0.0.1", AuthMethod: AuthMethodAgent, SSHAgentSocket: "/s.sock"}).
		SSHConfig(Settings{Username: "deploy"})
	if !agent.UseAgent || agent.AgentSocket != "/s.sock" || agent.KeyPath != "" {
		t.Errorf("agent SSHConfig = %+v", agent)
	}
	if agent.Address != "ssh://deploy@10.0.0.1" {
		t.Errorf("agent address = %q, want ssh://deploy@10.0.0.1", agent.Address)
	}

	// Key mode: carries the resolved per-host key, not the agent.
	key := (&HostConfig{Address: "h2", SSHKey: "/host/key"}).SSHConfig(Settings{})
	if key.UseAgent || key.KeyPath != "/host/key" {
		t.Errorf("key SSHConfig = %+v", key)
	}

	// Inference: a global key cascades to a host with none.
	inherited := (&HostConfig{Address: "h3"}).SSHConfig(Settings{SSHKey: "/global/key"})
	if inherited.UseAgent || inherited.KeyPath != "/global/key" {
		t.Errorf("inherited-key SSHConfig = %+v", inherited)
	}
}

func TestValidateAuthMethod(t *testing.T) {
	// Bad enum value is rejected.
	cfg := &Config{Hosts: map[string]*HostConfig{"h": {Address: "a", AuthMethod: "bogus"}}}
	if errs := Validate(cfg); len(errs) == 0 {
		t.Error("expected an error for an invalid auth_method")
	}
	// auth_method: key with no key anywhere is rejected.
	cfg2 := &Config{Hosts: map[string]*HostConfig{"h": {Address: "a", AuthMethod: AuthMethodKey}}}
	if errs := Validate(cfg2); len(errs) == 0 {
		t.Error("expected an error for auth_method=key with no ssh_key")
	}
	// Valid agent host with no socket is fine.
	cfg3 := &Config{Hosts: map[string]*HostConfig{"h": {Address: "a", AuthMethod: AuthMethodAgent}}}
	if errs := Validate(cfg3); len(errs) != 0 {
		t.Errorf("unexpected errors for valid agent host: %v", errs)
	}
}
