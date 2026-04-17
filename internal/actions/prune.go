package actions

import (
	"context"

	internalssh "github.com/AhmedAburady/marina/internal/ssh"
)

// PruneOptions selects which resources `marina prune` removes. When all
// flags are false the remote command is a plain `docker system prune -f`
// (stopped containers, dangling images, unused networks, build cache).
type PruneOptions struct {
	ImagesOnly bool // dangling images only
	ImagesAll  bool // ALL unused images (dangling + tagged)
	Volumes    bool // unused volumes
}

// PruneCommand returns the docker command string that corresponds to the
// given options. Exposed so callers (CLI confirm messages, TUI details
// panel) can preview exactly what will run.
func PruneCommand(opts PruneOptions) string {
	switch {
	case opts.ImagesAll:
		return "docker image prune -af"
	case opts.ImagesOnly:
		return "docker image prune -f"
	case opts.Volumes:
		return "docker volume prune -f"
	default:
		return "docker system prune -f"
	}
}

// Prune runs the remote prune command for one host and returns the combined
// stdout+stderr plus any exec error. Callers orchestrate confirmation +
// per-host loops.
func Prune(ctx context.Context, sshCfg internalssh.Config, opts PruneOptions) (string, error) {
	return internalssh.Exec(ctx, sshCfg, PruneCommand(opts))
}
