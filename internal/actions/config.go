package actions

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/AhmedAburady/marina/internal/config"
)

// SetConfigKey applies a single key=value change to cfg in memory.
// The caller is responsible for calling config.Save() afterwards.
//
// Supported keys:
//
//	username             global default SSH username
//	ssh_key              global default SSH key path (tilde and $VAR are expanded on next load)
//	prune_after_update   bool: true/false/yes/no/1/0
//	gotify.url           Gotify server URL
//	gotify.token         Gotify app token (plaintext)
//	gotify.priority      Gotify notification priority (integer)
func SetConfigKey(cfg *config.Config, key, value string) error {
	switch strings.ToLower(key) {
	case "username":
		cfg.Settings.Username = value
	case "ssh_key":
		cfg.Settings.SSHKey = value
	case "prune_after_update":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("prune_after_update: expected true/false, got %q", value)
		}
		cfg.Settings.PruneAfterUpdate = b
	case "gotify.url":
		cfg.Notify.Gotify.URL = value
	case "gotify.token":
		cfg.Notify.Gotify.Token = value
	case "gotify.priority":
		if value == "" {
			cfg.Notify.Gotify.Priority = 0
			return nil
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("gotify.priority: expected integer, got %q", value)
		}
		cfg.Notify.Gotify.Priority = n
	default:
		return fmt.Errorf("unknown config key %q (supported: username, ssh_key, prune_after_update, gotify.url, gotify.token, gotify.priority)", key)
	}
	return nil
}
