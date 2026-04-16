// Package ui provides styled terminal table renderers for marina output.
package ui

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/AhmedAburady/marina/internal/discovery"
	"github.com/docker/docker/api/types/container"
)

var (
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	cellStyle   = lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1)
	borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// StyledTable returns a pre-configured table with rounded borders and padding.
func StyledTable(headers ...string) *table.Table {
	return table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(borderStyle).
		BorderHeader(true).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle.PaddingLeft(1).PaddingRight(1)
			}
			return cellStyle
		}).
		Headers(headers...)
}

// PrintContainerTable writes a styled table of containers to w.
// Columns: HOST | NAME | IMAGE | STATUS | PORTS
func PrintContainerTable(w io.Writer, host string, containers []container.Summary) {
	t := StyledTable("HOST", "NAME", "IMAGE", "STATUS", "PORTS")

	for _, c := range containers {
		name := containerName(c)
		ports := formatPorts(c.Ports)
		t.Row(host, name, c.Image, c.Status, ports)
	}

	fmt.Fprint(w, t.String())
	fmt.Fprintln(w)
}

// PrintStackTable writes a styled table of compose stacks to w.
// Columns: HOST | STACK | DIR | CONTAINERS
func PrintStackTable(w io.Writer, stacks []discovery.Stack) {
	t := StyledTable("HOST", "STACK", "DIR", "CONTAINERS")

	for _, s := range stacks {
		t.Row(s.Host, s.Name, s.Dir, strconv.Itoa(len(s.Containers)))
	}

	fmt.Fprint(w, t.String())
	fmt.Fprintln(w)
}

// containerName returns the primary name of a container with the leading
// slash stripped (Docker always prefixes container names with "/").
func containerName(c container.Summary) string {
	if len(c.Names) == 0 {
		return c.ID[:12]
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

// formatPorts formats the exposed port mappings as "hostPort->containerPort/proto".
// At most 2 ports are shown to keep the table readable.
func formatPorts(ports []container.Port) string {
	var parts []string
	for _, p := range ports {
		if p.PublicPort == 0 {
			// Port is not bound to the host — skip.
			continue
		}
		parts = append(parts, fmt.Sprintf("%d->%d/%s", p.PublicPort, p.PrivatePort, p.Type))
		if len(parts) == 2 {
			break
		}
	}
	return strings.Join(parts, ", ")
}
