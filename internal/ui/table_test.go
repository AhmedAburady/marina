package ui

// Golden tests for PrintContainerTable and PrintStackTable.
//
// Width: termWidth() calls term.GetSize(os.Stdout.Fd()); under `go test` stdout
// is a pipe, term.GetSize returns an error, so the fallback of 80 applies.
// Goldens are captured at width=80. The COLUMNS env var is never read by this
// package, so setting it has no effect.
//
// Colors: lipgloss v2 inspects the color profile at rendering time, not at
// package init, so the actual ANSI sequences depend on the terminal environment
// at test execution time. Goldens are captured and compared verbatim — running
// under a TTY will produce different output than under a pipe. Always run
// `go test -update` from a non-TTY environment (CI / piped) to regenerate.
//
// To regenerate golden files:
//   go test -run TestPrintContainerTable -update ./internal/ui/
//   go test -run TestPrintStackTable -update ./internal/ui/

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/container"

	"github.com/AhmedAburady/marina/internal/discovery"
)

var update = flag.Bool("update", false, "update golden files")

// goldenPath returns the path to the golden file in testdata/.
func goldenPath(name string) string {
	return filepath.Join("testdata", name+".golden")
}

// checkGolden compares got against the golden file at name.golden.
// When -update is set it writes got to the file instead.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := goldenPath(name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdirall %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create)", path, err)
	}
	if string(want) != got {
		t.Errorf("output does not match golden %s\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

// captureContainerTable returns PrintContainerTable output as a string.
func captureContainerTable(host string, containers []container.Summary) string {
	var buf bytes.Buffer
	PrintContainerTable(&buf, host, containers)
	return buf.String()
}

// captureStackTable returns PrintStackTable output as a string.
func captureStackTable(stacks []discovery.Stack) string {
	var buf bytes.Buffer
	PrintStackTable(&buf, stacks)
	return buf.String()
}

// ── PrintContainerTable goldens ───────────────────────────────────────────────

func TestPrintContainerTable_Empty(t *testing.T) {
	got := captureContainerTable("testhost", []container.Summary{})
	checkGolden(t, "containers_empty", got)
}

func TestPrintContainerTable_Mixed(t *testing.T) {
	containers := []container.Summary{
		{
			ID:     "abc123456789",
			Names:  []string{"/web"},
			Image:  "nginx:latest",
			State:  "running",
			Status: "Up 2 hours",
			Labels: map[string]string{"com.docker.compose.project": "myapp"},
		},
		{
			ID:     "def123456789",
			Names:  []string{"/db"},
			Image:  "postgres:15",
			State:  "running",
			Status: "Up 2 hours",
			Labels: map[string]string{"com.docker.compose.project": "myapp"},
		},
		{
			ID:     "ghi123456789",
			Names:  []string{"/cache"},
			Image:  "redis:7",
			State:  "running",
			Status: "Up 1 hour",
			Labels: map[string]string{"com.docker.compose.project": "cache"},
		},
		{
			ID:     "jkl123456789",
			Names:  []string{"/old"},
			Image:  "nginx:1.20",
			State:  "exited",
			Status: "Exited (0) 1 day ago",
			Labels: map[string]string{},
		},
	}
	got := captureContainerTable("testhost", containers)
	checkGolden(t, "containers_mixed", got)
}

func TestPrintContainerTable_WideNames(t *testing.T) {
	// Long stack / container / image names that force column truncation.
	containers := []container.Summary{
		{
			ID:    "aaa111222333",
			Names: []string{"/very-long-container-name-that-exceeds-normal-column-width"},
			Image: "registry.example.com/org/very-long-image-name:with-a-long-tag-too",
			State: "running",
			Status: "Up 30 minutes",
			Labels: map[string]string{
				"com.docker.compose.project": "a-very-long-stack-name-here",
			},
		},
		{
			ID:    "bbb444555666",
			Names: []string{"/another-very-long-service-name-that-is-quite-wide"},
			Image: "registry.example.com/org/another-image:latest-stable-release",
			State: "restarting",
			Status: "Restarting (1) 5 seconds ago",
			Labels: map[string]string{
				"com.docker.compose.project": "a-very-long-stack-name-here",
			},
		},
	}
	got := captureContainerTable("testhost-with-long-name", containers)
	checkGolden(t, "containers_wide_names", got)
}

// ── PrintStackTable goldens ───────────────────────────────────────────────────

func TestPrintStackTable_Sorted(t *testing.T) {
	// Running stacks first (alphabetical), stopped stacks last (alphabetical).
	stacks := []discovery.Stack{
		{Name: "zebra",    Host: "testhost", Dir: "/opt/zebra",    Running: 2, Total: 2},
		{Name: "alpha",    Host: "testhost", Dir: "/opt/alpha",    Running: 3, Total: 3},
		{Name: "beta",     Host: "testhost", Dir: "/opt/beta",     Running: 0, Total: 2},
		{Name: "degraded", Host: "testhost", Dir: "/opt/degraded", Running: 1, Total: 3},
		{Name: "also-stopped", Host: "testhost", Dir: "/opt/also-stopped", Running: 0, Total: 1},
	}
	// NOTE: PrintStackTable does NOT re-sort; it preserves the caller's order.
	// The discovery package returns stacks pre-sorted (running first, then by
	// name). We pass them pre-sorted here to match production behavior.
	sorted := []discovery.Stack{
		{Name: "alpha",    Host: "testhost", Dir: "/opt/alpha",    Running: 3, Total: 3},
		{Name: "degraded", Host: "testhost", Dir: "/opt/degraded", Running: 1, Total: 3},
		{Name: "zebra",    Host: "testhost", Dir: "/opt/zebra",    Running: 2, Total: 2},
		{Name: "also-stopped", Host: "testhost", Dir: "/opt/also-stopped", Running: 0, Total: 1},
		{Name: "beta",     Host: "testhost", Dir: "/opt/beta",     Running: 0, Total: 2},
	}
	_ = stacks // documented above

	got := captureStackTable(sorted)
	checkGolden(t, "stacks_sorted", got)
}
