package tui

import (
	"image/color"
	"os"
	"strings"
)

// ── Row grouping ──────────────────────────────────────────────────────────
//
// Lists render as borderless column layouts — no lipgloss table, no grid.
// This file owns the row-grouping primitives that let Containers / Stacks /
// Updates split their flat row list into per-host sections without
// repeating the host cell on every data line.

// tableGroup is a labeled run of consecutive rows sharing a group column
// value (typically HOST). Label is rendered once as a bold-green heading
// above the group's rows; Rows carry the per-row cell values with the
// group column already stripped.
//
// RowColors, when set, overrides the default foreground colour for the
// corresponding row in Rows. Must be same length as Rows or nil. `nil`
// entries inside RowColors fall back to the default row style (zebra +
// white text). Used by the Stacks screen to tint stopped / degraded rows
// the same way the CLI does.
type tableGroup struct {
	Label     string
	Rows      [][]string
	FirstRow  int
	RowColors []color.Color
}

// groupRows partitions a flat row list into consecutive runs sharing the
// value in column `col`. The grouped column is stripped from each row so
// it isn't rendered redundantly on every line.
func groupRows(rows [][]string, col int) []tableGroup {
	if col < 0 {
		return []tableGroup{{Rows: rows}}
	}
	var groups []tableGroup
	prev := ""
	for i, r := range rows {
		if col >= len(r) {
			continue
		}
		label := r[col]
		stripped := make([]string, 0, len(r)-1)
		stripped = append(stripped, r[:col]...)
		stripped = append(stripped, r[col+1:]...)
		if len(groups) == 0 || label != prev {
			groups = append(groups, tableGroup{Label: label, FirstRow: i})
			prev = label
		}
		last := len(groups) - 1
		groups[last].Rows = append(groups[last].Rows, stripped)
	}
	return groups
}

// ── Value helpers ──────────────────────────────────────────────────────────

// shortPath collapses $HOME → "~" in a path so long absolute paths read
// cleanly in tables.
func shortPath(p string) string {
	if p == "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}
