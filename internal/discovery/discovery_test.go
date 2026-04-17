package discovery

import (
	"testing"

	"github.com/docker/docker/api/types/container"
)

// makeContainer is a convenience constructor for container.Summary with only
// the fields GroupByStack inspects.
func makeContainer(project, workingDir, state string) container.Summary {
	labels := map[string]string{}
	if project != "" {
		labels[labelProject] = project
	}
	if workingDir != "" {
		labels[labelWorkingDir] = workingDir
	}
	return container.Summary{
		Labels: labels,
		State:  state,
	}
}

// stackByName is a helper that returns the first Stack with the given name or
// nil if not found.
func stackByName(stacks []Stack, name string) *Stack {
	for i := range stacks {
		if stacks[i].Name == name {
			return &stacks[i]
		}
	}
	return nil
}

// ── GroupByStack table-driven cases ──────────────────────────────────────────

// TestGroupByStack_RunningContainersSameProject verifies that multiple running
// containers sharing a compose project label are merged into one Stack with
// correct Running and Total counts.
func TestGroupByStack_RunningContainersSameProject(t *testing.T) {
	containers := []container.Summary{
		makeContainer("webapp", "/opt/webapp", "running"),
		makeContainer("webapp", "/opt/webapp", "running"),
		makeContainer("webapp", "/opt/webapp", "running"),
	}

	stacks := GroupByStack("host1", containers, nil)

	if len(stacks) != 1 {
		t.Fatalf("len(stacks) = %d, want 1", len(stacks))
	}
	s := stacks[0]
	if s.Name != "webapp" {
		t.Errorf("Name = %q, want %q", s.Name, "webapp")
	}
	if s.Running != 3 {
		t.Errorf("Running = %d, want 3", s.Running)
	}
	if s.Total != 3 {
		t.Errorf("Total = %d, want 3", s.Total)
	}
	if s.Host != "host1" {
		t.Errorf("Host = %q, want %q", s.Host, "host1")
	}
	if s.Dir != "/opt/webapp" {
		t.Errorf("Dir = %q, want %q", s.Dir, "/opt/webapp")
	}
}

// TestGroupByStack_MixedStatesSameProject verifies that a project with both
// running and exited containers produces a single Stack where Running reflects
// only the running containers.
func TestGroupByStack_MixedStatesSameProject(t *testing.T) {
	containers := []container.Summary{
		makeContainer("db", "/opt/db", "running"),
		makeContainer("db", "/opt/db", "exited"),
		makeContainer("db", "/opt/db", "exited"),
	}

	stacks := GroupByStack("host2", containers, nil)

	if len(stacks) != 1 {
		t.Fatalf("len(stacks) = %d, want 1", len(stacks))
	}
	s := stacks[0]
	if s.Running != 1 {
		t.Errorf("Running = %d, want 1", s.Running)
	}
	if s.Total != 3 {
		t.Errorf("Total = %d, want 3", s.Total)
	}
}

// TestGroupByStack_NoComposeLabel verifies that containers without a compose
// project label are silently ignored — they produce no Stack entry.
func TestGroupByStack_NoComposeLabel(t *testing.T) {
	containers := []container.Summary{
		makeContainer("", "", "running"),          // no labels at all
		makeContainer("myapp", "/opt/app", "running"), // one compose container
	}

	stacks := GroupByStack("host3", containers, nil)

	if len(stacks) != 1 {
		t.Fatalf("len(stacks) = %d, want 1; loose containers must be ignored", len(stacks))
	}
	if stacks[0].Name != "myapp" {
		t.Errorf("Name = %q, want %q", stacks[0].Name, "myapp")
	}
}

// TestGroupByStack_ConfiguredStoppedStackAppears verifies that a stack listed
// in configStacks but having no running containers still appears in the result.
func TestGroupByStack_ConfiguredStoppedStackAppears(t *testing.T) {
	// No running containers, but "infra" is in the config map.
	containers := []container.Summary{
		makeContainer("webapp", "/opt/webapp", "running"),
	}
	configStacks := map[string]string{
		"infra": "/opt/infra",
	}

	stacks := GroupByStack("host4", containers, configStacks)

	if len(stacks) != 2 {
		t.Fatalf("len(stacks) = %d, want 2", len(stacks))
	}

	infra := stackByName(stacks, "infra")
	if infra == nil {
		t.Fatal("infra stack not found in results")
	}
	if infra.Dir != "/opt/infra" {
		t.Errorf("infra.Dir = %q, want %q", infra.Dir, "/opt/infra")
	}
	if infra.Running != 0 {
		t.Errorf("infra.Running = %d, want 0 (fully stopped)", infra.Running)
	}
	if infra.Total != 0 {
		t.Errorf("infra.Total = %d, want 0 (no containers discovered)", infra.Total)
	}
}

// TestGroupByStack_ConfigFallbackDir verifies that when a container's
// working_dir label is empty, the configured directory is used instead.
func TestGroupByStack_ConfigFallbackDir(t *testing.T) {
	containers := []container.Summary{
		// working_dir label absent
		makeContainer("myapp", "", "running"),
	}
	configStacks := map[string]string{
		"myapp": "/configured/path",
	}

	stacks := GroupByStack("host5", containers, configStacks)

	if len(stacks) != 1 {
		t.Fatalf("len(stacks) = %d, want 1", len(stacks))
	}
	if stacks[0].Dir != "/configured/path" {
		t.Errorf("Dir = %q, want %q", stacks[0].Dir, "/configured/path")
	}
}

// TestGroupByStack_SortOrder verifies the documented sort order:
// running stacks first (alphabetical), stopped stacks last (alphabetical).
func TestGroupByStack_SortOrder(t *testing.T) {
	containers := []container.Summary{
		makeContainer("zebra", "/opt/z", "running"),
		makeContainer("alpha", "/opt/a", "running"),
		makeContainer("middle", "/opt/m", "exited"), // stopped (no running container)
	}
	configStacks := map[string]string{
		"aardvark": "/opt/aa", // stopped-only configured stack
	}

	stacks := GroupByStack("host6", containers, configStacks)

	// Expected order: alpha(running), zebra(running), aardvark(stopped), middle(stopped)
	expected := []string{"alpha", "zebra", "aardvark", "middle"}
	if len(stacks) != len(expected) {
		names := make([]string, len(stacks))
		for i, s := range stacks {
			names[i] = s.Name
		}
		t.Fatalf("len(stacks) = %d, want %d; got %v", len(stacks), len(expected), names)
	}
	for i, want := range expected {
		if stacks[i].Name != want {
			t.Errorf("stacks[%d].Name = %q, want %q", i, stacks[i].Name, want)
		}
	}
}

// TestGroupByStack_MultipleProjects verifies that containers from distinct
// compose projects produce separate Stack entries.
func TestGroupByStack_MultipleProjects(t *testing.T) {
	containers := []container.Summary{
		makeContainer("frontend", "/opt/fe", "running"),
		makeContainer("backend", "/opt/be", "running"),
		makeContainer("backend", "/opt/be", "running"),
	}

	stacks := GroupByStack("host7", containers, nil)

	if len(stacks) != 2 {
		t.Fatalf("len(stacks) = %d, want 2", len(stacks))
	}

	fe := stackByName(stacks, "frontend")
	be := stackByName(stacks, "backend")

	if fe == nil || be == nil {
		t.Fatal("expected both 'frontend' and 'backend' stacks")
	}
	if fe.Total != 1 {
		t.Errorf("frontend.Total = %d, want 1", fe.Total)
	}
	if be.Total != 2 {
		t.Errorf("backend.Total = %d, want 2", be.Total)
	}
}

// TestGroupByStack_EmptyInput verifies the nil/empty container list returns
// an empty (non-nil) slice — callers should not need to nil-check the return.
func TestGroupByStack_EmptyInput(t *testing.T) {
	stacks := GroupByStack("host8", nil, nil)
	if stacks == nil {
		t.Error("GroupByStack returned nil, want empty slice")
	}
	if len(stacks) != 0 {
		t.Errorf("len(stacks) = %d, want 0", len(stacks))
	}
}
