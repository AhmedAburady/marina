package tui

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// ── TUI logger ─────────────────────────────────────────────────────────────
//
// Actions run from the dashboard are otherwise invisible — SSH commands
// fire, results come back as tea.Msg, and any silent failure (pull ran,
// up -d no-op'd, etc.) just shows as "no change" in the UI. This logger
// writes a durable audit trail to ~/.config/marina/marina.log so users
// can see what happened:
//
//   tail -f ~/.config/marina/marina.log
//
// The logger is lazily initialised on first use. If the log file can't be
// opened (permissions, missing dir) logging silently becomes a no-op so
// the TUI never fails just because it couldn't write to disk.

var (
	logOnce sync.Once
	logger  *slog.Logger
	logFile *os.File
)

// Log returns the package logger. First call opens
// ~/.config/marina/marina.log; subsequent calls reuse the same handle.
// The returned logger is always usable — it discards silently on setup
// failure.
func Log() *slog.Logger {
	logOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			logger = slog.New(slog.DiscardHandler)
			return
		}
		dir := filepath.Join(home, ".config", "marina")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			logger = slog.New(slog.DiscardHandler)
			return
		}
		f, err := os.OpenFile(
			filepath.Join(dir, "marina.log"),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND,
			0o600,
		)
		if err != nil {
			logger = slog.New(slog.DiscardHandler)
			return
		}
		logFile = f
		logger = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	return logger
}

// shortenErr extracts the first line of an error message, capped at
// `maxLen` runes. Keeps log entries readable without multi-line dumps.
func shortenErr(err error, maxLen int) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	for i, r := range s {
		if r == '\n' {
			s = s[:i]
			break
		}
	}
	if maxLen > 0 && len(s) > maxLen {
		s = s[:maxLen] + "…"
		_ = fmt.Sprintf // keep fmt import usable for future helpers
	}
	return s
}
