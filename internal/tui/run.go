package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/AhmedAburady/marina/internal/config"
)

// Run launches the full-screen dashboard. The ctx is used both to bound the
// Bubble Tea program lifecycle (via tea.WithContext) and, separately, to
// propagate cancellation into per-tab commands that capture it (see
// hostTestCmd for an example).
func Run(ctx context.Context, cfg *config.Config) error {
	m := newDashboard(ctx, cfg)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	_, err := p.Run()
	return err
}
