// Package actions is the single source of truth for every mutation Marina
// performs on its config or on a remote Docker host. Both the cobra
// subcommands (commands/*) and the TUI screens (internal/tui/*) consume
// these functions so behaviour stays identical and we never have to edit
// the same logic in two places.
//
// Functions in this package are pure in the sense that they take primitive
// or config-level inputs and return (result, error). They never touch the
// terminal (no huh, no bubbletea, no fmt.Fprintln), never read flags, and
// never write output. Callers are free to decorate them with spinners,
// tea.Cmds, or plain stdout as fits their UI.
package actions
