package actions

import (
	"context"
	"io"

	internalssh "github.com/AhmedAburady/marina/internal/ssh"
)

// ApplyStackUpdate runs `docker compose pull` then `docker compose up -d` as
// two distinct operations with stop-on-error semantics — mirroring the TUI's
// SequenceCmds(pull, up) flow. Output (combined stdout+stderr from each step)
// is written to w as it arrives; pass io.Discard for silent operation.
// Returns the first non-nil error from either step; the second step is
// skipped when the first fails.
func ApplyStackUpdate(ctx context.Context, sshCfg internalssh.Config, dir string, w io.Writer) error {
	out, err := ComposeOp(ctx, sshCfg, dir, "pull")
	if w != nil && out != "" {
		_, _ = io.WriteString(w, out)
	}
	if err != nil {
		return err
	}

	out, err = ComposeOp(ctx, sshCfg, dir, "up -d")
	if w != nil && out != "" {
		_, _ = io.WriteString(w, out)
	}
	return err
}

// PruneHost runs `docker image prune -f` on the remote host. Output is
// written to w (pass io.Discard for quiet operation).
func PruneHost(ctx context.Context, sshCfg internalssh.Config, w io.Writer) error {
	out, err := internalssh.Exec(ctx, sshCfg, "docker image prune -f")
	if w != nil && out != "" {
		_, _ = io.WriteString(w, out)
	}
	return err
}
