// Package ui provides styled terminal table renderers for marina output.
package ui

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

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
	stoppedStyle = lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1).Foreground(lipgloss.Color("183")) // muted mauve/pink
)

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

// PrintContainerTable writes a host header followed by a styled table of containers.
func PrintContainerTable(w io.Writer, host string, containers []container.Summary) {
	fmt.Fprintln(w, hostHeader(host))
	t := StyledTable("NAME", "IMAGE", "STATUS", "PORTS")

	for _, c := range containers {
		name := containerName(c)
		ports := formatPorts(c.Ports)
		t.Row(name, c.Image, c.Status, ports)
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
				if row >= 0 && row < len(hostStacks) && hostStacks[row].Total == 0 {
					return stoppedStyle
				}
				return cellStyle
			}).
			Headers("STACK", "DIR", "STATUS")
		for _, s := range hostStacks {
			t.Row(s.Name, s.Dir, stackStatus(s))
		}
		fmt.Fprintln(w, t.String())
	}
}

// stackStatus returns a human-readable status string for a stack.
// Examples: "8/8 running", "7/8 running", "stopped"
func stackStatus(s discovery.Stack) string {
	if s.Total == 0 {
		return "stopped"
	}
	if s.Running == s.Total {
		return strconv.Itoa(s.Running) + " running"
	}
	return strconv.Itoa(s.Running) + "/" + strconv.Itoa(s.Total) + " running"
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

// newPlainWriter returns a tabwriter that aligns columns with consistent padding.
func newPlainWriter(w io.Writer) *tabwriter.Writer {
	// minwidth=0, tabwidth=4, padding=3, padchar=' '
	return tabwriter.NewWriter(w, 0, 4, 3, ' ', 0)
}

// PrintContainerTablePlain writes a host header and aligned columns.
func PrintContainerTablePlain(w io.Writer, host string, containers []container.Summary) {
	fmt.Fprintf(w, "[%s]\n", host)
	tw := newPlainWriter(w)
	fmt.Fprintln(tw, "NAME\tIMAGE\tSTATUS\tPORTS")
	for _, c := range containers {
		name := containerName(c)
		ports := formatPorts(c.Ports)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, c.Image, c.Status, ports)
	}
	tw.Flush()
	fmt.Fprintln(w)
}

// PrintStackTablePlain writes stacks grouped by host with aligned columns.
func PrintStackTablePlain(w io.Writer, stacks []discovery.Stack) {
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
		fmt.Fprintf(w, "[%s]\n", host)
		tw := newPlainWriter(w)
		fmt.Fprintln(tw, "STACK\tDIR\tSTATUS")
		for _, s := range grouped[host] {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Name, s.Dir, stackStatus(s))
		}
		tw.Flush()
	}
}

// PrintHostTablePlain writes hosts as aligned columns with a header.
func PrintHostTablePlain(w io.Writer, rows [][]string) {
	tw := newPlainWriter(w)
	fmt.Fprintln(tw, "NAME\tUSER\tADDRESS\tKEY")
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	tw.Flush()
}
