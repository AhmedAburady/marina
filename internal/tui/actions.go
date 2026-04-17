package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/AhmedAburady/marina/internal/actions"
	internalssh "github.com/AhmedAburady/marina/internal/ssh"
)

// ActionResultMsg is delivered to a screen's Update when an action finishes.
// Kind identifies the operation (e.g. "container.start", "compose.pull"),
// Target is the row key the action was invoked on, Output captures the
// combined stdout+stderr, and Err is non-nil when the SSH call failed or
// the remote command exited non-zero.
type ActionResultMsg struct {
	Kind   string
	Target string
	Output string
	Err    error
}

// SequenceResultsMsg carries the per-step results of a SequenceCmds run.
// Steps appear in execution order; if a step failed, its ActionResultMsg
// is the last entry (subsequent cmds are short-circuited).
type SequenceResultsMsg struct {
	Results []ActionResultMsg
}

// DockerExecCmd runs `docker <verb> <target>` on the remote host via the
// shared actions layer. `kind` and `target` are echoed back in the result
// message so the screen can map the outcome to the right row.
//
// Note: historically this accepted a free-form command string; that form is
// retained for compatibility with the Updates screen's multi-step pull+up.
// New callers should prefer actions.ContainerOp / actions.ComposeOp.
func DockerExecCmd(ctx context.Context, sshCfg internalssh.Config, command, kind, target string) tea.Cmd {
	return func() tea.Msg {
		Log().Info("action.start", "kind", kind, "target", target, "host", sshCfg.Address, "cmd", command)
		out, err := rawExec(ctx, sshCfg, command)
		if err != nil {
			Log().Warn("action.fail", "kind", kind, "target", target, "err", shortenErr(err, 200), "out", firstChars(out, 200))
		} else {
			Log().Info("action.ok", "kind", kind, "target", target, "out", firstChars(out, 200))
		}
		return ActionResultMsg{Kind: kind, Target: target, Output: out, Err: err}
	}
}

// ComposeExecCmd runs `cd <dir> && docker compose <subCmd>` via the shared
// actions layer. Wrapper kept for tea.Cmd ergonomics; the underlying logic
// lives in actions.ComposeOp and is used by the CLI too. Each call logs
// start / ok|fail to ~/.config/marina/marina.log with the full composed
// command and the first chunk of stdout so users can diagnose silent
// "apply did nothing" cases via `tail -f`.
func ComposeExecCmd(ctx context.Context, sshCfg internalssh.Config, dir, subCmd, kind, target string) tea.Cmd {
	return func() tea.Msg {
		Log().Info("compose.start", "kind", kind, "target", target, "host", sshCfg.Address, "dir", dir, "sub", subCmd)
		out, err := actions.ComposeOp(ctx, sshCfg, dir, subCmd)
		if err != nil {
			Log().Warn("compose.fail", "kind", kind, "target", target, "err", shortenErr(err, 200), "out", firstChars(out, 400))
		} else {
			Log().Info("compose.ok", "kind", kind, "target", target, "out", firstChars(out, 400))
		}
		return ActionResultMsg{Kind: kind, Target: target, Output: out, Err: err}
	}
}

// firstChars returns up to n runes of s — safe to log command output
// snippets without bloating the audit log.
func firstChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// rawExec is a tiny ssh helper used by DockerExecCmd's free-form path. For
// anything structured (container verbs, compose subcommands) go through the
// actions package instead.
func rawExec(ctx context.Context, sshCfg internalssh.Config, command string) (string, error) {
	return internalssh.Exec(ctx, sshCfg, command)
}

// SequenceCmds runs the given tea.Cmds in order, stopping on the first
// ActionResultMsg whose Err is non-nil. All collected results — including
// the failing step when present — are delivered as a single
// SequenceResultsMsg so the caller can aggregate outcomes in one handler.
//
// Neither tea.Sequence nor tea.Batch has built-in stop-on-error; purge
// needs it, and the stop-on-error contract prevents half-applied updates.
func SequenceCmds(cmds ...tea.Cmd) tea.Cmd {
	if len(cmds) == 0 {
		return nil
	}
	return func() tea.Msg {
		results := make([]ActionResultMsg, 0, len(cmds))
		for _, c := range cmds {
			if c == nil {
				continue
			}
			msg := c()
			res, ok := msg.(ActionResultMsg)
			if !ok {
				results = append(results, ActionResultMsg{
					Err: fmt.Errorf("sequence step returned %T, want ActionResultMsg", msg),
				})
				break
			}
			results = append(results, res)
			if res.Err != nil {
				break
			}
		}
		return SequenceResultsMsg{Results: results}
	}
}

