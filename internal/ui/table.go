// Package ui provides styled terminal table renderers for marina output.
package ui

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/AhmedAburady/marina/internal/discovery"
	"github.com/charmbracelet/x/term"
	"github.com/docker/docker/api/types/container"
)

var (
	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	cellStyle    = lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1)
	borderStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	stoppedStyle  = lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1).Foreground(lipgloss.Color("183")) // muted mauve/pink
	degradedStyle = lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1).Foreground(lipgloss.Color("214")) // amber/orange — needs attention

	// Container-state row tints — mirror the TUI containers screen so the CLI
	// and dashboard show the same colour for the same state.
	containerExitedStyle     = lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1).Foreground(lipgloss.Color("9"))  // bright red — exited / dead
	containerRestartingStyle = lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1).Foreground(lipgloss.Color("11")) // bright yellow — restarting
	containerDimStyle        = lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1).Foreground(lipgloss.Color("8"))  // dim grey — paused / created
)

// containerStateStyle returns the lipgloss style for a container row based
// on its docker `State` field. Running returns nil so the default cell
// style applies (no colour override). Matches tui.containerStateColor.
func containerStateStyle(state string) *lipgloss.Style {
	switch state {
	case "exited", "dead":
		return &containerExitedStyle
	case "restarting":
		return &containerRestartingStyle
	case "paused", "created":
		return &containerDimStyle
	}
	return nil
}

// termWidth returns the current terminal width, falling back to 80 if it
// cannot be determined (e.g. when stdout is not a TTY).
func termWidth() int {
	w, _, err := term.GetSize(os.Stdout.Fd())
	if err != nil || w <= 0 {
		return 80
	}
	return w
}

// StyledTable returns a pre-configured table with rounded borders and padding,
// constrained to the current terminal width.
func StyledTable(headers ...string) *table.Table {
	return table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(borderStyle).
		BorderHeader(true).
		Width(termWidth()).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle.PaddingLeft(1).PaddingRight(1)
			}
			return cellStyle
		}).
		Headers(headers...)
}

// hostHeader returns a styled host name header line.
func hostHeader(host string) string {
	style := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	return style.Render("▸ " + host)
}

// PrintContainerTable writes a host header followed by a styled table of containers
// sorted by stack name for visual grouping. Rows are tinted by docker state:
// exited/dead → red, restarting → yellow, paused/created → dim, running →
// default. Same palette the TUI containers screen uses.
func PrintContainerTable(w io.Writer, host string, containers []container.Summary) {
	// Sort containers by stack name, then by container name within each stack.
	sort.Slice(containers, func(i, j int) bool {
		si := containers[i].Labels["com.docker.compose.project"]
		sj := containers[j].Labels["com.docker.compose.project"]
		if si != sj {
			return si < sj
		}
		return ContainerName(containers[i]) < ContainerName(containers[j])
	})

	fmt.Fprintln(w, hostHeader(host))
	// Closure-capture the sorted slice so StyleFunc can look up the state
	// for each rendered data row by index (row 0..len-1).
	sorted := containers
	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(borderStyle).
		BorderHeader(true).
		Width(termWidth()).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle.PaddingLeft(1).PaddingRight(1)
			}
			if row >= 0 && row < len(sorted) {
				if st := containerStateStyle(sorted[row].State); st != nil {
					return *st
				}
			}
			return cellStyle
		}).
		Headers("STACK", "CONTAINER", "IMAGE", "STATUS", "PORTS")

	for _, c := range sorted {
		stack := c.Labels["com.docker.compose.project"]
		if stack == "" {
			stack = "-"
		}
		name := ContainerName(c)
		ports := FormatPorts(c.Ports)
		t.Row(stack, name, c.Image, c.Status, ports)
	}

	fmt.Fprintln(w, t.String())
}

// PrintStackTable writes stacks grouped by host, each with its own header and table.
func PrintStackTable(w io.Writer, stacks []discovery.Stack) {
	// Group stacks by host, preserving insertion order.
	var hostOrder []string
	grouped := make(map[string][]discovery.Stack)
	for _, s := range stacks {
		if _, seen := grouped[s.Host]; !seen {
			hostOrder = append(hostOrder, s.Host)
		}
		grouped[s.Host] = append(grouped[s.Host], s)
	}

	for i, host := range hostOrder {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, hostHeader(host))
		hostStacks := grouped[host]
		t := table.New().
			Border(lipgloss.RoundedBorder()).
			BorderStyle(borderStyle).
			BorderHeader(true).
			Width(termWidth()).
			StyleFunc(func(row, col int) lipgloss.Style {
				if row == table.HeaderRow {
					return headerStyle.PaddingLeft(1).PaddingRight(1)
				}
				// row is 0-indexed for data rows
				if row >= 0 && row < len(hostStacks) {
					s := hostStacks[row]
					if s.Running == 0 {
						return stoppedStyle
					}
					if s.Running < s.Total {
						return degradedStyle
					}
				}
				return cellStyle
			}).
			Headers("STACK", "DIR", "STATUS")
		for _, s := range hostStacks {
			t.Row(s.Name, s.Dir, StackStatus(s))
		}
		fmt.Fprintln(w, t.String())
	}
}

// StackStatus returns a human-readable status string for a stack.
// Examples: "8/8 running", "7/8 running", "stopped"
func StackStatus(s discovery.Stack) string {
	if s.Running == 0 {
		return "stopped"
	}
	if s.Running == s.Total {
		return strconv.Itoa(s.Running) + " running"
	}
	return strconv.Itoa(s.Running) + "/" + strconv.Itoa(s.Total) + " running"
}

// ContainerName returns the primary name of a container with the leading
// slash stripped (Docker always prefixes container names with "/").
func ContainerName(c container.Summary) string {
	if len(c.Names) == 0 {
		return c.ID[:12]
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

// FormatPorts formats the exposed port mappings as "hostPort->containerPort/proto".
// At most 2 ports are shown to keep the table readable.
func FormatPorts(ports []container.Port) string {
	seen := make(map[string]bool)
	var parts []string
	for _, p := range ports {
		if p.PublicPort == 0 {
			continue
		}
		entry := fmt.Sprintf("%d->%d/%s", p.PublicPort, p.PrivatePort, p.Type)
		if seen[entry] {
			continue
		}
		seen[entry] = true
		parts = append(parts, entry)
		if len(parts) == 2 {
			break
		}
	}
	return strings.Join(parts, ", ")
}

