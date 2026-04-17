package actions

import (
	"fmt"
	"strings"
)

// HostStackErr is a single per-host (optionally per-stack) failure.
type HostStackErr struct {
	Host  string
	Stack string // empty for host-level ops like prune
	Err   error
}

func (e *HostStackErr) Error() string {
	if e.Stack != "" {
		return fmt.Sprintf("%s/%s: %v", e.Host, e.Stack, e.Err)
	}
	return fmt.Sprintf("%s: %v", e.Host, e.Err)
}
func (e *HostStackErr) Unwrap() error { return e.Err }

// ApplyErr aggregates per-host failures for a multi-host operation.
type ApplyErr struct {
	Op       string // "update", "prune", ...
	Failures []HostStackErr
}

func (e *ApplyErr) Error() string {
	if len(e.Failures) == 0 {
		return fmt.Sprintf("%s: no failures", e.Op)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d failure(s):", e.Op, len(e.Failures))
	for _, f := range e.Failures {
		fmt.Fprintf(&b, "\n  - %s", f.Error())
	}
	return b.String()
}

// Unwrap returns child errors for errors.Is / errors.As traversal (Go 1.20+).
func (e *ApplyErr) Unwrap() []error {
	out := make([]error, len(e.Failures))
	for i := range e.Failures {
		out[i] = &e.Failures[i]
	}
	return out
}

// NewApplyErr returns a non-nil *ApplyErr iff there is at least one failure,
// otherwise returns nil — lets callers write `return actions.NewApplyErr(...)`.
func NewApplyErr(op string, failures []HostStackErr) error {
	if len(failures) == 0 {
		return nil
	}
	return &ApplyErr{Op: op, Failures: failures}
}
