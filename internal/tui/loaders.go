package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/AhmedAburady/marina/internal/actions"
	"github.com/AhmedAburady/marina/internal/config"
)

// HostFetchResult is the TUI-facing alias for actions.HostFetchResult. Kept
// as a type alias so existing screen code doesn't need import shuffles
// every time we extend the action layer.
type HostFetchResult = actions.HostFetchResult

// HostsFetchedMsg is delivered by FetchAllHostsCmd once every target has
// resolved (live data, cache, or terminal error).
type HostsFetchedMsg struct {
	Results map[string]HostFetchResult
}

// FetchAllHostsCmd wraps actions.FetchAllHosts as a tea.Cmd. The fan-out
// itself lives in the actions package so `marina ps` / `marina stacks` and
// the TUI consume the exact same implementation.
func FetchAllHostsCmd(
	ctx context.Context,
	cfg *config.Config,
	targets map[string]*config.HostConfig,
) tea.Cmd {
	return func() tea.Msg {
		return HostsFetchedMsg{Results: actions.FetchAllHosts(ctx, cfg, targets)}
	}
}
