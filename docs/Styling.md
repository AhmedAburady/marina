# Marina TUI Styling Guide — lipgloss v2 + bubbles v2

A practical guide to styling without surprises. Written after hands-on
debugging of real bugs in marina's settings screen and confirmed against the
official charmbracelet upgrade guides, v2 blog posts, and a forum thread
where the maintainers explicitly confirmed the underlying limitations.

> **Primary source of truth:** lipgloss v2 renders one ANSI run per `Render()`
> call. Concatenating styled strings concatenates their ANSI → the outer fill
> breaks wherever an inner `reset` lands.

---

## 1. Mental model

### 1.1 Styles are pure values
`lipgloss.NewStyle()` returns a value type. There is **no renderer**. Every
`Style.Render(s)` emits a self-contained sequence:

```
<set-style> <s> <reset>
```

The reset at the end is unconditional.

### 1.2 Concatenation is not composition
```go
// Looks innocent. Is dangerous.
panelStyle.Render(a.Render("x") + " " + b.Render("y"))
```

What the terminal actually sees:

```
[panel-on] [a-on]x[reset] [b-on]y[reset] [panel-off]
              ^^^^^        ^^^^^
              these resets kill panel's background mid-row
```

The outer style does **not re-apply** after an inner reset. It only
surrounds the content. Between the inner reset and the next styled span,
the terminal falls back to its default background. That's the "black leak"
bug.

### 1.3 Lipgloss v1 is 2D only. v2 adds optional compositing.
From a maintainer-confirmed forum exchange
([golangbridge #39271](https://forum.golangbridge.org/t/bubbletea-and-stacking-styles-with-lipgloss/39271)):

> Lipgloss library is 2D only. Stacking styles over other is simply not
> implemented. Logic for overlapping content is absent.

v2 introduces **Canvas + Layer + Compositor** to solve overlap/stacking
([lipgloss-v2-beta-2 blog post](https://charm.land/blog/lipgloss-v2-beta-2/)).
Marina already uses this in `internal/tui/overlay.go` for modals.

---

## 2. The Prime Directive

> **One `Style.Render()` per row.** Whatever gets concatenated into a single
> row must be built such that no `reset` lands mid-row — either by keeping
> inner styles' backgrounds identical to the row's background, or by
> wrapping the whole row in a single outer `Render()` whose content is plain
> unstyled text.

If you can't build a row as a single `Render()` call, pad each span to its
target width and keep every span's background consistent.

---

## 3. Anti-patterns and fixes

### A1. Bare separators between styled spans

```go
// BAD — " " has no style, becomes a bg hole inside a filled container
return yesPill.Render("Yes") + " " + noPill.Render("No")
```

```go
// GOOD — style the gap to match the surrounding fill
gap := rowStyle.Render(" ")
return yesPill.Render("Yes") + gap + noPill.Render("No")
```

### A2. Concatenating styled components inside a bg-filled container

```go
// BAD — inner Renders punch holes in the outer fill
card.Render(textinput.View() + " " + indicator.Render("*"))
```

**Rule:** inside a filled container, either

- build the content from plain strings and let ONE outer `Render()` apply the
  fill, or
- ensure every inner `Render()` uses a style whose `Background()` is
  identical to the container's `Background()`.

### A3. `bubbles/textinput.View()` on blurred inputs (virtualCursor bg leak)

**The bug** (verified against
`charm.land/bubbles/v2@v2.1.0/textinput/textinput.go:704-719` and
`cursor/cursor.go:236`):

- `textinput.Model.View()` always appends `m.virtualCursor.View()` after the
  text, focused or blurred.
- The virtual cursor's blurred render path is
  `TextStyle.Inline(true).Render(m.char)`.
- `textinput` never populates `TextStyle` — `updateVirtualCursorStyle` only
  writes `m.virtualCursor.Style = NewStyle().Foreground(Styles.Cursor.Color)`.
  `TextStyle` stays at its zero value.
- A zero-value `Render(" ")` emits a bare space with a trailing reset, which
  terminates the surrounding row's background.
- There is **no public API** to set `TextStyle`.

**The fix** (pattern from `internal/tui/settings.go:valueRow`):

```go
// Focused → use textinput so the live cursor and typing work
if s.focus == rowIdx {
    return row(width, s.inputs[rowIdx].View())
}

// Blurred → render the value manually with your own styled span
raw := s.inputs[rowIdx].Value()
if raw == "" {
    return row(width, placeholderStyle.Render(s.inputs[rowIdx].Placeholder))
}
if s.inputs[rowIdx].EchoMode == textinput.EchoPassword {
    raw = strings.Repeat(string(s.inputs[rowIdx].EchoCharacter),
        utf8.RuneCountInString(raw))
}
return row(width, valueStyle.Render(raw))
```

Also set both `Focused.Text` and `Blurred.Text` on the textinput's styles
with an explicit `Background()` that matches the surrounding fill.

### A4. `lipgloss.Place` on ragged multi-line content

```go
// BAD — Place centres each line on its own width, so lines of different
// widths land at different x-coords. Labels and inputs end up misaligned.
lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, labelsAndInputs)
```

```go
// GOOD — render as a single rigid block first (all lines same width),
// then Place. The block becomes one rectangle with consistent left edge.
block := cardStyle.Width(cardWidth).Render(content)
lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, block)
```

When you must centre multi-line content without wrapping it in a Width
style, pad every line to the same target width yourself before joining.

### A5. Overlays without Canvas + Layer

String-level composition (`bg + "\n" + modal`) eats the background beneath
the modal. Use v2's compositor (already used in `internal/tui/overlay.go`):

```go
base := lipgloss.NewLayer(bg)
top := lipgloss.NewLayer(modal).X(x).Y(y).Z(1)
comp := lipgloss.NewCompositor(base, top)
canvas := lipgloss.NewCanvas(width, height)
canvas.Compose(comp)
return canvas.Render()
```

The canvas rasterizes each cell with a definite final style, so inner
resets cannot leak.

### A6. Missing `Background()` on inner text styles

Every style that renders text **inside** a filled container needs an
explicit `Background()` matching the container. Foreground-only styles
emit resets that break the fill.

```go
// BAD — inside a cPanel card, this label has no bg
labelStyle := lipgloss.NewStyle().Foreground(cFg).Bold(true)

// GOOD — explicit bg means the label's ANSI run preserves the fill
labelStyle := lipgloss.NewStyle().
    Background(cPanel).
    Foreground(cFg).
    Bold(true)
```

### A7. `Inline(true)` with vertical padding

`Inline(true)` strips vertical padding silently. Don't pair them.

### A8. Space key handling regression from bubbletea v1

In v2 the space bar is `"space"`, not `" "`. Check `msg.String() == "space"`.
Tests or pre-v2 code may still match `" "` and fail silently.

---

## 4. Bubbles v2 integration quirks

### textinput

- **Set `Prompt = ""`** if you don't want the default `"> "` prompt. Its
  rendered width counts against your column, not against `SetWidth(n)`,
  causing overflow and the `panelLine` truncator to emit `…`.
- **Set both `Focused.Text.Background` and `Blurred.Text.Background`** to
  match the surrounding fill, otherwise the input's internal padding
  (`strings.Repeat(" ", padding)`) shows the wrong bg.
- **Set `Focused.Placeholder.Background` and `Blurred.Placeholder.Background`**
  for the same reason — placeholder view follows separate styles.
- **`Styles.Cursor.Color` only sets `Foreground`**; there is no way to set
  the cursor's background. The blurred cursor will always emit a 1-cell
  reset inside the input's rendered output → always bypass `View()` for
  blurred inputs (see A3).
- **`SetVirtualCursor(false)`** hides the virtual cursor entirely. Useful
  when the parent Screen can't propagate the real `tea.Cursor`.

### spinner / list / viewport

Same rule applies — any component whose `View()` you concatenate into a
styled row must either (a) be the only styled span on the row, or (b) have
its inner styles backgrounded to match the row's fill.

---

## 5. Marina house conventions

### 5.1 Palette (`internal/tui/styles.go`)
- `cBg` (`#000000`) — app/body background. Pure black by design: same as
  the terminal default, so a missing `Background()` doesn't visually leak
  in the default body.
- `cPanel` (`#1a1a1a`) — zebra alt-row fill, also used for subtle surfaces.
- `cOverlay` (`#262626`) — modal and card surfaces; lighter than `cPanel`
  so cards read as a distinct "window" on the body.
- `cInput` (`#0a0a0a`) — text-input fill inside modals.
- `cSelected` / `cAccent` (`#7D56F4`) — brand purple, used for selected
  rows and active pills.
- `cTeal` (`#38BDF8`) — focus/active signal for labels, titles, cursor.
- `cFg`, `cDim`, `cMuted` — text foregrounds by emphasis.
- `cGreen` / `cYellow` / `cRed` / `cOrange` — semantic status.

### 5.2 No borders. Fill everywhere.
Marina's TUI is intentionally border-free. Definition comes from
background contrast (`cBg` → `cPanel` → `cOverlay`), not lines. Do not add
`Border()` to any style.

### 5.3 Focus signalling
Colour change only. **No arrow / prefix characters on labels** — they
shift the x-origin of the label relative to its value and break vertical
alignment. Pattern: dim+bold when blurred, teal+bold when focused.

### 5.4 Cards (filled "windows")
Use `cOverlay` background. Rigid fixed `Width()`. `Padding(1, 3)` or
similar for breathing room. Centre the card with `lipgloss.Place` —
because the card is a rigid rectangle, Place centres it correctly.

See `internal/tui/settings.go` for the reference implementation.

### 5.5 Screens
Every screen implements the `Screen` interface in `internal/tui/screen.go`
and returns `View(width, height) string` — NOT `tea.View`. The top-level
dashboard model converts to `tea.View` once. This means individual
screens cannot attach a real `tea.Cursor`; rely on the bubbles virtual
cursor for focused inputs.

### 5.6 Keys — bubbletea v2 specifics
- Match space with `"space"`, not `" "`.
- Key events arrive as `tea.KeyPressMsg` (v2 split press/release).
- For forms: **Enter saves, Tab/↓ moves focus**. This matches marina's
  convention in the settings screen.

### 5.7 Modal composition
All modals go through `overlayModal()` in `internal/tui/overlay.go` which
uses Canvas+Layer+Compositor. Never hand-roll string overlay.

---

## 6. Audit history

### Round 1 — initial audit (fixed)

Three latent bg-leak anti-patterns detected. All three worked visually only
because `cBg == #000000` matched the terminal's default background — if the
body were ever changed to a non-black fill, all three would have leaked.
Fixed preemptively:

| Location | Issue | Fix |
|---|---|---|
| `list.go:summaryLine` | bare `"    "` between plain `bold` and `sMuted.Render(meta)` inside `panelLine(sSuccess, …)` | gap now styled with `sSuccess.Render("    ")` |
| `overlay.go:renderConfirmModal` | bare `"  "` between two `renderPill` outputs | gap now uses new `sDialogGap` helper (`Background(cBg)`) |
| `overlay.go:renderSpinnerModal` | bare `"  "` between bubbles spinner and `sDialogTitle.Render(title)` | same `sDialogGap` helper |
| `overlay.go:renderPill` (idle) | foreground-only style (no `Background`) → padding cells leaked terminal default | added `Background(cBg)` |
| `overlay.go` dialog styles | `sDialogTitle`/`Label`/`Focus`/`Err`/`Help`/`Dim` were foreground-only | all now carry explicit `Background(cBg)` |

The `sDialogGap` helper was introduced in this round as the canonical
pattern for bare separators inside `modalBox`.

---

## 7. Pre-merge checklist for TUI changes

Before opening a PR that touches `internal/tui/`:

- [ ] No bare separators between styled spans (grep for
      `\.Render\([^)]*\)\s*\+\s*"` and `"\s*\+\s*\w+\.Render\(`).
- [ ] Every text style rendered inside a filled container has an explicit
      `Background()` matching the container.
- [ ] Any `bubbles/textinput` in a filled container uses the manual render
      path for blurred state (see `settings.valueRow`).
- [ ] `lipgloss.Place` only wraps rigid single-block content (or every
      line is pre-padded to the same width).
- [ ] Modals/overlays go through `overlayModal()` — no string-based overlay.
- [ ] Space key is matched as `"space"`, not `" "`.
- [ ] No borders added; definition comes from fills.
- [ ] Focus on labels conveyed by colour only; no arrow prefixes that
      shift label x-origin relative to the value row.

---

## 8. Reference links

- [Lip Gloss v2 What's New (discussion #506)](https://github.com/charmbracelet/lipgloss/discussions/506)
- [Lip Gloss v2 Upgrade Guide](https://github.com/charmbracelet/lipgloss/blob/main/UPGRADE_GUIDE_V2.md)
- [Lip Gloss v2 Beta 2 blog post — compositing intro](https://charm.land/blog/lipgloss-v2-beta-2/)
- [Bubble Tea v2 What's New (discussion #1374)](https://github.com/charmbracelet/bubbletea/discussions/1374)
- [Bubble Tea v2 Upgrade Guide](https://github.com/charmbracelet/bubbletea/blob/main/UPGRADE_GUIDE_V2.md)
- [Charm v2 major releases blog post](https://charm.land/blog/v2/)
- [Forum thread: lipgloss 2D limitation confirmed](https://forum.golangbridge.org/t/bubbletea-and-stacking-styles-with-lipgloss/39271)
- [Lipgloss canvas compositing example](https://github.com/charmbracelet/lipgloss/blob/main/examples/canvas/main.go)
- [Bubbles textinput source — virtualCursor bug site](https://github.com/charmbracelet/bubbles/blob/main/textinput/textinput.go)
- [Charm docs — Bubble Tea styling guide](https://www.mintlify.com/charmbracelet/bubbletea/guides/styling)
