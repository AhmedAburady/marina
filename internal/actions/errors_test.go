package actions

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ── NewApplyErr ───────────────────────────────────────────────────────────────

// TestNewApplyErr_NilOnNilSlice verifies the zero-failure fast path returns nil.
func TestNewApplyErr_NilOnNilSlice(t *testing.T) {
	if err := NewApplyErr("prune", nil); err != nil {
		t.Errorf("NewApplyErr with nil failures = %v, want nil", err)
	}
}

// TestNewApplyErr_NilOnEmptySlice verifies that an explicitly-allocated but
// empty failures slice also returns nil (len()==0 branch).
func TestNewApplyErr_NilOnEmptySlice(t *testing.T) {
	if err := NewApplyErr("prune", []HostStackErr{}); err != nil {
		t.Errorf("NewApplyErr with empty failures = %v, want nil", err)
	}
}

// TestNewApplyErr_NonNilOnFailures verifies that at least one failure yields
// a non-nil *ApplyErr.
func TestNewApplyErr_NonNilOnFailures(t *testing.T) {
	failures := []HostStackErr{
		{Host: "host-a", Err: fmt.Errorf("connection refused")},
	}
	err := NewApplyErr("update", failures)
	if err == nil {
		t.Fatal("NewApplyErr with failures = nil, want non-nil error")
	}
	var ae *ApplyErr
	if !errors.As(err, &ae) {
		t.Fatalf("errors.As(*ApplyErr) = false, want true")
	}
}

// ── errors.As traversal ───────────────────────────────────────────────────────

// TestApplyErr_ErrorsAs verifies that errors.As on the returned error recovers
// the *ApplyErr with the correct Op and Failures count.
func TestApplyErr_ErrorsAs(t *testing.T) {
	failures := []HostStackErr{
		{Host: "host-a", Err: fmt.Errorf("dial: timeout")},
		{Host: "host-b", Stack: "redis", Err: fmt.Errorf("compose up: exit 1")},
	}
	err := NewApplyErr("update", failures)

	var ae *ApplyErr
	if !errors.As(err, &ae) {
		t.Fatalf("errors.As(*ApplyErr) = false")
	}
	if ae.Op != "update" {
		t.Errorf("Op = %q, want %q", ae.Op, "update")
	}
	if len(ae.Failures) != 2 {
		t.Errorf("len(Failures) = %d, want 2", len(ae.Failures))
	}
}

// TestApplyErr_Unwrap verifies that Unwrap() returns one error per failure and
// that errors.As on each individual entry recovers a *HostStackErr.
func TestApplyErr_Unwrap(t *testing.T) {
	failures := []HostStackErr{
		{Host: "host-a", Err: fmt.Errorf("timeout")},
		{Host: "host-b", Stack: "nginx", Err: fmt.Errorf("oom")},
	}
	err := NewApplyErr("prune", failures)

	var ae *ApplyErr
	if !errors.As(err, &ae) {
		t.Fatalf("errors.As(*ApplyErr) = false")
	}

	unwrapped := ae.Unwrap()
	if len(unwrapped) != len(failures) {
		t.Fatalf("Unwrap() len = %d, want %d", len(unwrapped), len(failures))
	}

	for i, child := range unwrapped {
		var hse *HostStackErr
		if !errors.As(child, &hse) {
			t.Errorf("unwrapped[%d]: errors.As(*HostStackErr) = false", i)
			continue
		}
		if hse.Host != failures[i].Host {
			t.Errorf("unwrapped[%d].Host = %q, want %q", i, hse.Host, failures[i].Host)
		}
	}
}

// ── Error() string content ────────────────────────────────────────────────────

// TestApplyErr_ErrorString verifies the Error() string contains the op name,
// failure count, and every host name.
func TestApplyErr_ErrorString(t *testing.T) {
	failures := []HostStackErr{
		{Host: "alpha", Err: fmt.Errorf("unreachable")},
		{Host: "beta", Stack: "postgres", Err: fmt.Errorf("oom")},
	}
	err := NewApplyErr("update", failures)
	msg := err.Error()

	checkContains := func(needle string) {
		t.Helper()
		if !strings.Contains(msg, needle) {
			t.Errorf("Error() does not contain %q\nfull: %s", needle, msg)
		}
	}

	checkContains("update")
	checkContains("2")      // failure count
	checkContains("alpha")
	checkContains("beta")
}

// ── HostStackErr.Error() formatting ──────────────────────────────────────────

// TestHostStackErr_NoStack verifies "host: err" format when Stack is empty.
func TestHostStackErr_NoStack(t *testing.T) {
	e := &HostStackErr{Host: "myhost", Err: fmt.Errorf("ssh: refused")}
	got := e.Error()
	want := "myhost: ssh: refused"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	// Must not contain a trailing slash.
	if strings.Contains(got, "/") {
		t.Errorf("Error() contains '/' but Stack is empty: %q", got)
	}
}

// TestHostStackErr_WithStack verifies "host/stack: err" format when Stack is set.
func TestHostStackErr_WithStack(t *testing.T) {
	e := &HostStackErr{Host: "myhost", Stack: "redis", Err: fmt.Errorf("exit 2")}
	got := e.Error()
	want := "myhost/redis: exit 2"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestHostStackErr_Unwrap verifies that errors.Unwrap on HostStackErr returns
// the original wrapped error.
func TestHostStackErr_Unwrap(t *testing.T) {
	sentinel := fmt.Errorf("disk full")
	e := &HostStackErr{Host: "host", Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Error("errors.Is(HostStackErr, sentinel) = false, want true")
	}
}
