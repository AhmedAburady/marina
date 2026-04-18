package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/AhmedAburady/marina/internal/config"
)

// homeScreen is the root of the navigation stack. It shows the Marina banner
// and a centered menu of destinations. Enter/→ pushes the matching section;
// q / ctrl+c quits.
type homeScreen struct {
	ctx    context.Context
	cfg    *config.Config
	cursor int
	items  []homeItem
}

// homeItem is a destination entry. `build` returns a fresh Screen on each
// push so the user can re-enter a section without stale state.
type homeItem struct {
	title    string
	subtitle string
	build    func(ctx context.Context, cfg *config.Config) Screen
}

func newHomeScreen(ctx context.Context, cfg *config.Config) *homeScreen {
	return &homeScreen{
		ctx: ctx,
		cfg: cfg,
		items: []homeItem{
			{"Hosts", "Browse configured hosts and test SSH connectivity",
				func(ctx context.Context, cfg *config.Config) Screen { return newHostsScreen(ctx, cfg) }},
			{"Containers", "List every running container across all hosts",
				func(ctx context.Context, cfg *config.Config) Screen { return newContainersScreen(ctx, cfg) }},
			{"Stacks", "Manage Docker Compose stacks: start, stop, pull, purge",
				func(ctx context.Context, cfg *config.Config) Screen { return newStacksScreen(ctx, cfg) }},
			{"Updates", "Check image registries for available updates",
				func(ctx context.Context, cfg *config.Config) Screen { return newUpdatesScreen(ctx, cfg) }},
			{"Prune", "Remove unused Docker resources across hosts",
				func(ctx context.Context, cfg *config.Config) Screen { return newPruneScreen(ctx, cfg) }},
			{"Settings", "Edit global SSH, update, and notification settings",
				func(ctx context.Context, cfg *config.Config) Screen { return newSettingsScreen(ctx, cfg) }},
		},
	}
}

func (s *homeScreen) Title() string { return "Home" }
func (s *homeScreen) Init() tea.Cmd { return nil }
func (s *homeScreen) Help() string  { return "↑/↓ select · enter open · q quit" }

func (s *homeScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q":
			return s, tea.Quit
		case "up", "k":
			if s.cursor > 0 {
				s.cursor--
			}
		case "down", "j":
			if s.cursor < len(s.items)-1 {
				s.cursor++
			}
		case "enter", "right", "l":
			item := s.items[s.cursor]
			return s, pushCmd(item.build(s.ctx, s.cfg))
		}
	}
	return s, nil
}

func (s *homeScreen) View(width, height int) string {
	// Build the content block top-down, THEN prepend blank rows to centre it
	// vertically inside the (width × height) panel. Every row we push is
	// exactly `width` chars wide with the black panel bg; panelLines handles
	// any final padding if rounding leaves one row short at the bottom.
	content := s.buildContent(width)

	// Vertical centre: half the slack goes above, half below.
	top := max0((height - len(content)) / 2)
	out := make([]string, 0, height)
	for range top {
		out = append(out, panelLine(sPanel, width, ""))
	}
	out = append(out, content...)
	return panelLines(width, height, out)
}

// buildContent returns the banner + subtitle + menu + footer rows as a tight
// slice. Caller pads above/below to centre the whole block.
func (s *homeScreen) buildContent(width int) []string {
	var lines []string

	// ── Banner: coloured, centred horizontally via centerText so the block
	// tracks any terminal width. When the banner is wider than the screen
	// we fall back to a plain "MARINA" word mark so Home never goes blank.
	bw := bannerWidth()
	if width >= bw+2 {
		for _, bl := range renderBanner() {
			lines = append(lines, panelLine(sPanel, width, centerText(bl, width)))
		}
	} else {
		fallback := lipgloss.NewStyle().
			Background(cPanel).
			Foreground(cAccent).
			Bold(true).
			Render("M A R I N A")
		lines = append(lines, panelLine(sPanel, width, ""))
		lines = append(lines, panelLine(sPanel, width, centerText(fallback, width)))
	}

	// ── Subtitle under the banner. Adaptive: shorter form on narrow
	// terminals so the text never clips or runs into the banner fallback.
	subtitle := "Multi-host Docker management"
	if width < 44 {
		subtitle = "Docker fleet"
	}
	lines = append(lines, panelLine(sPanel, width, ""))
	lines = append(lines, panelLine(sMuted, width, centerText(subtitle, width)))
	lines = append(lines, panelLine(sPanel, width, ""))

	// ── Menu: one row per item. Selected item gets the accent bg bar; a
	// blank spacer row between items gives the menu breathing room.
	for i, item := range s.items {
		blockW := min(60, width-8)
		if blockW < 20 {
			blockW = 20
		}
		outerPad := max0((width - blockW) / 2)

		label := "    " + item.title
		style := sHomeCard
		if i == s.cursor {
			label = "    ▸ " + item.title
			style = sHomeCardSelected
		}
		rowBody := padRight(label, blockW)
		row := strings.Repeat(" ", outerPad) + style.Render(rowBody) + strings.Repeat(" ", max0(width-outerPad-blockW))
		lines = append(lines, panelLine(sPanel, width, row))

		if i == s.cursor {
			sub := "         " + item.subtitle
			subLine := strings.Repeat(" ", outerPad) + padRight(sub, blockW)
			lines = append(lines, panelLine(sMuted, width, subLine))
		} else {
			lines = append(lines, panelLine(sPanel, width, ""))
		}
		lines = append(lines, panelLine(sPanel, width, ""))
	}

	// ── Footer: host summary (centred).
	lines = append(lines, panelLine(sPanel, width, ""))
	lines = append(lines, panelLine(sMuted, width, centerText(describeConfig(s.cfg), width)))

	return lines
}

// describeConfig builds a short summary line for the Home footer.
func describeConfig(cfg *config.Config) string {
	hosts := len(cfg.Hosts)
	switch hosts {
	case 0:
		return "No hosts configured — add one with: marina hosts add <name> <address>"
	case 1:
		return "1 host configured"
	default:
		return fmt.Sprintf("%d hosts configured", hosts)
	}
}
