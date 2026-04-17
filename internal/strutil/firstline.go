// Package strutil provides small string-manipulation helpers shared across
// the marina CLI and TUI layers.
package strutil

import "strings"

// FirstLine returns the first line of s, truncated to maxRunes runes (not
// bytes) if longer. An ellipsis "…" is appended when truncation happened.
// maxRunes <= 0 means unlimited.
func FirstLine(s string, maxRunes int) string {
	first, _, _ := strings.Cut(s, "\n")
	first = strings.TrimRight(first, "\r \t")
	if maxRunes <= 0 {
		return first
	}
	r := []rune(first)
	if len(r) <= maxRunes {
		return first
	}
	return string(r[:maxRunes]) + "…"
}
