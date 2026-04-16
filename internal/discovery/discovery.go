// Package discovery extracts Docker Compose stack information from container labels.
package discovery

import (
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
)

const (
	labelProject    = "com.docker.compose.project"
	labelWorkingDir = "com.docker.compose.project.working_dir"
)

// Stack represents a Docker Compose project on a single host.
type Stack struct {
	Name       string
	Dir        string // compose project working directory on the remote host
	Host       string // name of the host this stack is running on
	Containers []container.Summary
	Running    int // count of containers in "running" state
	Total      int // total containers (running + stopped)
}

// GroupByStack groups containers by their Docker Compose project label.
//
// configStacks is the host's manually configured stack map (name → dir) used
// as a reference, but since we only have running containers here we derive
// stack info from labels directly.
func GroupByStack(host string, containers []container.Summary, configStacks map[string]string) []Stack {
	type entry struct {
		dir        string
		containers []container.Summary
		running    int
	}

	groups := make(map[string]*entry)

	for _, c := range containers {
		project := c.Labels[labelProject]
		if project == "" {
			// Container is not part of a compose project; skip.
			continue
		}

		e, ok := groups[project]
		if !ok {
			dir := c.Labels[labelWorkingDir]
			if dir == "" {
				// Fall back to the manually configured stack directory.
				dir = configStacks[project]
			}
			e = &entry{dir: dir}
			groups[project] = e
		}
		e.containers = append(e.containers, c)
		if strings.HasPrefix(c.State, "running") {
			e.running++
		}
	}

	// Include config-defined stacks that have no running containers (stopped stacks).
	for name, dir := range configStacks {
		if _, seen := groups[name]; !seen {
			groups[name] = &entry{dir: dir}
		}
	}

	stacks := make([]Stack, 0, len(groups))
	for name, e := range groups {
		stacks = append(stacks, Stack{
			Name:       name,
			Dir:        e.dir,
			Host:       host,
			Containers: e.containers,
			Running:    e.running,
			Total:      len(e.containers),
		})
	}

	// Sort: running stacks first (by name), stopped stacks last (by name).
	sort.Slice(stacks, func(i, j int) bool {
		iStopped := stacks[i].Total == 0
		jStopped := stacks[j].Total == 0
		if iStopped != jStopped {
			return !iStopped // running before stopped
		}
		return stacks[i].Name < stacks[j].Name
	})

	return stacks
}
