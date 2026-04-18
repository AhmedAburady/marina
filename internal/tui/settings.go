package tui

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/AhmedAburady/marina/internal/actions"
	"github.com/AhmedAburady/marina/internal/config"
)

const (
	rowUsername    = 0
	rowSSHKey      = 1
	rowPrune       = 2
	rowGotifyURL   = 3
	rowGotifyToken = 4
	rowGotifyPrio  = 5
	rowSettingsMax = 6
)

// Card (window) geometry. The settings form lives inside a filled card whose
// lighter background differentiates it from the main body — no borders, pure
// fill. cOverlay (#262626) is lighter than cPanel (#1a1a1a) so it reads as a
// distinct surface.
const (
	cardWidth = 64
	cardPadX  = 3
	cardPadY  = 1
)

// cardInner is the drawable width inside the card (after horizontal padding).
// Every line we build pads to exactly this width so the ANSI run emitted by
// the card's Render call covers the whole row with one style — that's what
// prevents the 'bg leak' bugs documented in lipgloss v2 upgrade notes: each
// styled span emits a trailing reset, so to keep a fill uniform we aim for
// one span per row.
func cardInner() int { return cardWidth - 2*cardPadX }

var (
	cCard = cOverlay

	sFormCard = lipgloss.NewStyle().
			Background(cCard).
			Foreground(cFg).
			Width(cardWidth).
			Padding(cardPadY, cardPadX)

	sFormTitle = lipgloss.NewStyle().
			Background(cCard).
			Foreground(cTeal).
			Bold(true)

	sFormLabelBlur = lipgloss.NewStyle().
			Background(cCard).
			Foreground(cDim).
			Bold(true)

	sFormLabelFocus = lipgloss.NewStyle().
			Background(cCard).
			Foreground(cTeal).
			Bold(true)

	sFormFooter = lipgloss.NewStyle().
			Background(cCard).
			Foreground(cMuted)

	sFormValueBlur = lipgloss.NewStyle().
			Background(cCard).
			Foreground(cFg)

	sFormValuePlaceholder = lipgloss.NewStyle().
				Background(cCard).
				Foreground(cMuted).
				Italic(true)

	sFormCardLine = lipgloss.NewStyle().
			Background(cCard)

	sPillActive = lipgloss.NewStyle().
			Background(cAccent).
			Foreground(cWhite).
			Bold(true).
			Padding(0, 1)

	sPillIdle = lipgloss.NewStyle().
			Background(cPanel).
			Foreground(cDim).
			Padding(0, 1)
)

type settingsScreen struct {
	ctx     context.Context
	cfg     *config.Config
	focus   int
	inputs  [rowSettingsMax]textinput.Model
	prune   bool
	saveMsg string
}

func newSettingsScreen(ctx context.Context, cfg *config.Config) *settingsScreen {
	s := &settingsScreen{
		ctx:   ctx,
		cfg:   cfg,
		prune: cfg.Settings.PruneAfterUpdate,
	}

	// Inputs are sized to the card's inner width so every input row is the
	// same shape as every label row. When focused, the textinput owns the
	// row rendering (needed for the live cursor); when blurred we render the
	// value manually to avoid the virtualCursor bg-leak bug.
	inputWidth := cardInner()

	mkInput := func(placeholder, value string) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = placeholder
		ti.Prompt = ""
		ti.CharLimit = 256
		ti.SetWidth(inputWidth)
		ti.SetValue(value)
		st := ti.Styles()
		// Focused text and padding sit on the card bg so the focused row
		// paints a uniform fill (aside from the cursor cell, which always
		// resets — that's unavoidable without canvas compositing).
		st.Focused.Text = lipgloss.NewStyle().Background(cCard).Foreground(cFg)
		st.Focused.Placeholder = lipgloss.NewStyle().Background(cCard).Foreground(cMuted).Italic(true)
		st.Blurred.Text = lipgloss.NewStyle().Background(cCard).Foreground(cFg)
		st.Blurred.Placeholder = lipgloss.NewStyle().Background(cCard).Foreground(cMuted).Italic(true)
		st.Cursor.Color = cTeal
		ti.SetStyles(st)
		return ti
	}

	s.inputs[rowUsername] = mkInput("e.g. admin", cfg.Settings.Username)
	s.inputs[rowSSHKey] = mkInput("e.g. ~/.ssh/id_ed25519", config.ContractPath(cfg.Settings.SSHKey))
	s.inputs[rowGotifyURL] = mkInput("https://notify.example.com", cfg.Notify.Gotify.URL)
	s.inputs[rowGotifyToken] = mkInput("app token", cfg.Notify.Gotify.Token)
	s.inputs[rowGotifyToken].EchoMode = textinput.EchoPassword
	s.inputs[rowGotifyToken].EchoCharacter = '•'
	prio := ""
	if cfg.Notify.Gotify.Priority != 0 {
		prio = strconv.Itoa(cfg.Notify.Gotify.Priority)
	}
	s.inputs[rowGotifyPrio] = mkInput("e.g. 5", prio)

	s.inputs[rowUsername].Focus()
	return s
}

func (s *settingsScreen) Title() string { return "Settings" }
func (s *settingsScreen) Init() tea.Cmd { return textinput.Blink }

func (s *settingsScreen) Help() string {
	help := "tab/↑↓ navigate  ·  ←/→ toggle  ·  enter save  ·  esc back"
	if s.saveMsg != "" {
		help += "   " + s.saveMsg
	}
	return help
}

func (s *settingsScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		return s.handleKey(k)
	}
	if s.focus != rowPrune {
		newInput, cmd := s.inputs[s.focus].Update(msg)
		s.inputs[s.focus] = newInput
		return s, cmd
	}
	return s, nil
}

func (s *settingsScreen) handleKey(k tea.KeyPressMsg) (Screen, tea.Cmd) {
	switch k.String() {
	case "esc":
		return s, popCmd()
	case "enter":
		s.saveMsg = s.save()
		return s, nil
	case "up", "shift+tab":
		s.moveFocus(-1)
		return s, nil
	case "down", "tab":
		s.moveFocus(1)
		return s, nil
	case "left":
		if s.focus == rowPrune {
			s.prune = true
		}
		return s, nil
	case "right":
		if s.focus == rowPrune {
			s.prune = false
		}
		return s, nil
	case "space":
		if s.focus == rowPrune {
			s.prune = !s.prune
			return s, nil
		}
	}

	if s.focus != rowPrune {
		newInput, cmd := s.inputs[s.focus].Update(k)
		s.inputs[s.focus] = newInput
		return s, cmd
	}
	return s, nil
}

func (s *settingsScreen) moveFocus(dir int) {
	if s.focus != rowPrune {
		s.inputs[s.focus].Blur()
	}
	s.focus = (s.focus + dir + rowSettingsMax) % rowSettingsMax
	if s.focus != rowPrune {
		s.inputs[s.focus].Focus()
	}
	s.saveMsg = ""
}

func (s *settingsScreen) save() string {
	pairs := []struct{ key, val string }{
		{"username", s.inputs[rowUsername].Value()},
		{"ssh_key", s.inputs[rowSSHKey].Value()},
		{"prune_after_update", strconv.FormatBool(s.prune)},
		{"gotify.url", s.inputs[rowGotifyURL].Value()},
		{"gotify.token", s.inputs[rowGotifyToken].Value()},
		{"gotify.priority", s.inputs[rowGotifyPrio].Value()},
	}
	for _, p := range pairs {
		if err := actions.SetConfigKey(s.cfg, p.key, p.val); err != nil {
			return sError.Render("✗ " + err.Error())
		}
	}
	if err := config.Save(s.cfg, ""); err != nil {
		return sError.Render("✗ " + err.Error())
	}
	return sSuccess.Render("✓ Saved")
}

// View composes the form inside a filled card, then centres the card inside
// the body panel. The card has a lighter fill (cOverlay) so it stands out as
// a defined "window" on the black body — no borders anywhere.
func (s *settingsScreen) View(width, height int) string {
	card := sFormCard.Render(s.cardContents())
	return lipgloss.Place(
		width, height,
		lipgloss.Center, lipgloss.Center,
		card,
		lipgloss.WithWhitespaceStyle(sPanel),
	)
}

// cardContents renders the full form. Each content line is padded to the
// card's inner width and rendered through sFormCardLine so every row has a
// consistent cCard fill — no mid-row resets, no black leaks.
func (s *settingsScreen) cardContents() string {
	inner := cardInner()
	var rows []string

	rows = append(rows, row(inner, sFormTitle.Render("Settings")))
	rows = append(rows, blankRow(inner))

	rows = append(rows, s.labelRow(rowUsername, "Username", inner))
	rows = append(rows, s.valueRow(rowUsername, inner))
	rows = append(rows, blankRow(inner))

	rows = append(rows, s.labelRow(rowSSHKey, "SSH Key", inner))
	rows = append(rows, s.valueRow(rowSSHKey, inner))
	rows = append(rows, blankRow(inner))

	rows = append(rows, s.labelRow(rowPrune, "Prune After Update", inner))
	rows = append(rows, s.pillRow(inner))
	rows = append(rows, blankRow(inner))

	rows = append(rows, s.labelRow(rowGotifyURL, "Gotify URL", inner))
	rows = append(rows, s.valueRow(rowGotifyURL, inner))
	rows = append(rows, blankRow(inner))

	rows = append(rows, s.labelRow(rowGotifyToken, "Gotify Token", inner))
	rows = append(rows, s.valueRow(rowGotifyToken, inner))
	rows = append(rows, blankRow(inner))

	rows = append(rows, s.labelRow(rowGotifyPrio, "Gotify Priority", inner))
	rows = append(rows, s.valueRow(rowGotifyPrio, inner))
	rows = append(rows, blankRow(inner))

	cfgPath, _ := config.DefaultPath()
	rows = append(rows, row(inner, sFormFooter.Render("Config: "+config.ContractPath(cfgPath))))

	return strings.Join(rows, "\n")
}

// row wraps `content` in a card-background span sized to `width`, guaranteeing
// the whole row is one ANSI run. Content is not truncated — caller sizes it.
func row(width int, content string) string {
	return sFormCardLine.Width(width).Render(content)
}

// blankRow returns a card-bg row of empty content, used as vertical spacer.
func blankRow(width int) string {
	return sFormCardLine.Width(width).Render("")
}

// labelRow renders one field label. Focus is conveyed purely by colour — no
// arrow prefix, no extra indent, so the value row below lines up perfectly.
func (s *settingsScreen) labelRow(rowIdx int, text string, width int) string {
	style := sFormLabelBlur
	if s.focus == rowIdx {
		style = sFormLabelFocus
	}
	return row(width, style.Render(text))
}

// valueRow renders the value for a text input. When focused, the textinput's
// own View() is used so the live cursor and typing work. When blurred, the
// value is rendered manually to avoid the textinput virtualCursor's reset
// sequence (which would otherwise punch a cCard-sized hole into the fill).
func (s *settingsScreen) valueRow(rowIdx, width int) string {
	if s.focus == rowIdx {
		// Focused: let textinput render itself, then wrap so the row width
		// is exactly `width`. The textinput already pads to its own Width,
		// so no additional truncation needed.
		return row(width, s.inputs[rowIdx].View())
	}

	raw := s.inputs[rowIdx].Value()
	if raw == "" {
		return row(width, sFormValuePlaceholder.Render(s.inputs[rowIdx].Placeholder))
	}
	if s.inputs[rowIdx].EchoMode == textinput.EchoPassword {
		raw = strings.Repeat(string(s.inputs[rowIdx].EchoCharacter), utf8.RuneCountInString(raw))
	}
	return row(width, sFormValueBlur.Render(raw))
}

// pillRow renders the Yes/No toggle for the Prune field. The gap between the
// two pills is a cCard-styled space so the fill stays continuous across the
// row — a bare " " would leak black through.
func (s *settingsScreen) pillRow(width int) string {
	var yes, no string
	if s.prune {
		yes = sPillActive.Render("Yes")
		no = sPillIdle.Render("No")
	} else {
		yes = sPillIdle.Render("Yes")
		no = sPillActive.Render("No")
	}
	gap := sFormCardLine.Render(" ")
	return row(width, yes+gap+no)
}
