package ssh

import (
	"strings"
	"testing"
)

func TestFlags(t *testing.T) {
	tests := []struct {
		name        string
		cfg         Config
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:        "key mode pins the key and excludes the agent",
			cfg:         Config{KeyPath: "/home/u/.ssh/id_ed25519"},
			wantContain: []string{"-i /home/u/.ssh/id_ed25519", "IdentitiesOnly=yes"},
			wantAbsent:  []string{"IdentityAgent"},
		},
		{
			name:        "agent mode pins a specific socket",
			cfg:         Config{UseAgent: true, AgentSocket: "/tmp/agent.sock"},
			wantContain: []string{"IdentityAgent=/tmp/agent.sock"},
			wantAbsent:  []string{"IdentitiesOnly=yes", "-i "},
		},
		{
			name:       "agent mode without a socket falls back to SSH_AUTH_SOCK",
			cfg:        Config{UseAgent: true},
			wantAbsent: []string{"IdentityAgent", "IdentitiesOnly=yes", "-i "},
		},
		{
			name:       "no auth configured leaves identity selection to ssh",
			cfg:        Config{},
			wantAbsent: []string{"IdentityAgent", "IdentitiesOnly=yes", "-i "},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Join(Flags(tt.cfg), " ")
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("Flags(%+v) = %q\n want to contain %q", tt.cfg, got, want)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("Flags(%+v) = %q\n want NOT to contain %q", tt.cfg, got, absent)
				}
			}
			// Host-key verification must never be disabled by any auth mode.
			if !strings.Contains(got, "StrictHostKeyChecking=yes") {
				t.Errorf("Flags(%+v) = %q\n must always enforce StrictHostKeyChecking=yes", tt.cfg, got)
			}
		})
	}
}
