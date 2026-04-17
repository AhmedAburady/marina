package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// formField is one labeled text input inside a modal dialog. Rendering is
// handled in renderFormModal (overlay.go); this struct is just data + focus
// routing so the screens can keep their Update switch tidy.
type formField struct {
	label       string
	placeholder string
	required    bool
	input       textinput.Model
}

// newFormField builds a field with a configured placeholder + required flag.
// Caller focuses it via form.focusNext / focusPrev; input widths are set at
// render time in renderFormModal so fields size with the modal box.
func newFormField(label, placeholder string, required bool) formField {
	ti := textinput.New()
	ti.Placeholder = placeholder
	return formField{
		label:       label,
		placeholder: placeholder,
		required:    required,
		input:       ti,
	}
}

// inlineForm is a modal form. Screens embed this, feed tea.Msg through Update,
// and render via renderFormModal + overlayModal.
type inlineForm struct {
	title  string
	fields []formField
	focus  int
	err    string
}

// newInlineForm builds a blank form with the given title and fields. The
// first field is focused automatically so typing works immediately.
func newInlineForm(title string, fields []formField) *inlineForm {
	f := &inlineForm{title: title, fields: fields}
	if len(fields) > 0 {
		f.fields[0].input.Focus()
	}
	return f
}

// Value returns the trimmed value of the nth field.
func (f *inlineForm) Value(i int) string {
	if i < 0 || i >= len(f.fields) {
		return ""
	}
	return strings.TrimSpace(f.fields[i].input.Value())
}

// Update processes a bubbletea message. Returns (submit, cancel, cmd):
//   - submit == true: user pressed Enter with all required fields filled
//   - cancel == true: user pressed Esc
//   - cmd: forwarded to Bubble Tea for cursor blinks
func (f *inlineForm) Update(msg tea.Msg) (submit, cancel bool, cmd tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			return false, true, nil
		case "enter":
			if err := f.validate(); err != "" {
				f.err = err
				return false, false, nil
			}
			return true, false, nil
		case "tab", "down":
			f.focusNext()
			return false, false, nil
		case "shift+tab", "up":
			f.focusPrev()
			return false, false, nil
		}
	}
	// Delegate character input to the focused field only.
	if f.focus >= 0 && f.focus < len(f.fields) {
		newInput, c := f.fields[f.focus].input.Update(msg)
		f.fields[f.focus].input = newInput
		cmd = c
	}
	return false, false, cmd
}

// Modal returns the fully rendered modal string. Screens pass this straight
// to the dashboard via the ModalProvider interface.
func (f *inlineForm) Modal() string {
	const help = "tab move  ·  enter save  ·  esc cancel"
	return renderFormModal(f.title, f.fields, f.focus, f.err, help)
}

// ── Internals ───────────────────────────────────────────────────────────────

func (f *inlineForm) validate() string {
	for _, field := range f.fields {
		if field.required && strings.TrimSpace(field.input.Value()) == "" {
			return field.label + " is required"
		}
	}
	return ""
}

func (f *inlineForm) focusNext() {
	if len(f.fields) == 0 {
		return
	}
	f.fields[f.focus].input.Blur()
	f.focus = (f.focus + 1) % len(f.fields)
	f.fields[f.focus].input.Focus()
}

func (f *inlineForm) focusPrev() {
	if len(f.fields) == 0 {
		return
	}
	f.fields[f.focus].input.Blur()
	f.focus = (f.focus - 1 + len(f.fields)) % len(f.fields)
	f.fields[f.focus].input.Focus()
}
