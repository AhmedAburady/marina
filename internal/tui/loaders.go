package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/AhmedAburady/marina/internal/actions"
	"github.com/AhmedAburady/marina/internal/config"
)

// HostFetchResult is the TUI-facing alias for actions.HostFetchResult. Kept
// as a type alias so existing screen code doesn't need import shuffles every
// time we extend the action layer.
type HostFetchResult = actions.HostFetchResult

// HostFetchedMsg fires once per host as its individual fetch completes.
// Screens track received vs expected to detect when the overall fan-out is
// done; dead hosts appear within SSH ConnectTimeout (~10s) regardless of how
// long the healthy hosts take.
type HostFetchedMsg struct {
	Host   string
	Result HostFetchResult
}

// FetchAllHostsCmd kicks off one independent fetch per host. Each host
// dispatches its own HostFetchedMsg as it completes (or times out), so
// reachable hosts render immediately without waiting for the slowest host.
func FetchAllHostsCmd(
	ctx context.Context,
	cfg *config.Config,
	targets map[string]*config.HostConfig,
) tea.Cmd {
	if len(targets) == 0 {
		return nil
	}
	cmds := make([]tea.Cmd, 0, len(targets))
	for name, h := range targets {
		cmds = append(cmds, func() tea.Msg {
			return HostFetchedMsg{
				Host:   name,
				Result: actions.FetchHost(ctx, cfg, name, h),
			}
		})
	}
	return tea.Batch(cmds...)
}
