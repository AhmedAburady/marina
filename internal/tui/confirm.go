package tui

import tea "charm.land/bubbletea/v2"

// confirmPrompt is the two-pill destructive-action dialog shared by every
// screen that stages a "are you sure?" flow (Hosts → remove, Containers →
// remove, Stacks → purge / unregister, etc.). `focus` tracks which pill is
// active — 0 = Cancel, 1 = Confirm — and toggles on ←/→ / h / l / tab.
// `onYes` is invoked exactly once, only when the user lands on Confirm +
// presses Enter; Cancel and Esc close the dialog without firing it.
type confirmPrompt struct {
	title        string
	details      []string
	confirmLabel string
	focus        int
	onYes        func() tea.Cmd
}

// Update processes one incoming message and returns (done, cmd):
//
//   - done == true → caller should drop the prompt from its screen state
//     (s.prompt = nil, s.mode = …List). True on Confirm+Enter, Cancel+Enter,
//     and Esc.
//   - cmd          → non-nil ONLY when the user actually confirmed; callers
//     return it from their own Update.
//
// Non-key messages pass through with (false, nil) so the caller can still
// route them to background data loaders without interleaving bespoke modal
// logic per screen.
func (p *confirmPrompt) Update(msg tea.Msg) (done bool, cmd tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return false, nil
	}
	switch k.String() {
	case "left", "h", "shift+tab":
		p.focus = 0
	case "right", "l", "tab":
		p.focus = 1
	case "enter":
		if p.focus == 1 && p.onYes != nil {
			return true, p.onYes()
		}
		return true, nil
	case "esc":
		return true, nil
	}
	return false, nil
}

// View renders the dialog. Kept as a method so callers' Modal() can stay a
// single line regardless of how the overlay renderer evolves.
func (p *confirmPrompt) View() string {
	return renderConfirmModal(p.title, p.details, p.confirmLabel, p.focus)
}
