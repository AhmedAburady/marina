// Package discovery extracts Docker Compose stack information from container labels.
package discovery

import (
	"github.com/docker/docker/api/types/container"
)

const (
	labelProject    = "com.docker.compose.project"
	labelWorkingDir = "com.docker.compose.project.working_dir"
)

// Stack represents a Docker Compose project running on a single host.
type Stack struct {
	Name       string
	Dir        string // compose project working directory on the remote host
	Host       string // name of the host this stack is running on
	Containers []container.Summary
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
	}

	stacks := make([]Stack, 0, len(groups))
	for name, e := range groups {
		stacks = append(stacks, Stack{
			Name:       name,
			Dir:        e.dir,
			Host:       host,
			Containers: e.containers,
		})
	}
	return stacks
}
