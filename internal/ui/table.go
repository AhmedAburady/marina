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
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	cellStyle   = lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1)
	borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
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

// newPlainWriter returns a tabwriter that aligns columns with consistent padding.
func newPlainWriter(w io.Writer) *tabwriter.Writer {
	// minwidth=0, tabwidth=4, padding=3, padchar=' '
	return tabwriter.NewWriter(w, 0, 4, 3, ' ', 0)
}

// PrintContainerTablePlain writes containers as aligned columns with a header.
func PrintContainerTablePlain(w io.Writer, host string, containers []container.Summary) {
	tw := newPlainWriter(w)
	fmt.Fprintln(tw, "HOST\tNAME\tIMAGE\tSTATUS\tPORTS")
	for _, c := range containers {
		name := containerName(c)
		ports := formatPorts(c.Ports)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", host, name, c.Image, c.Status, ports)
	}
	tw.Flush()
}

// PrintStackTablePlain writes stacks as aligned columns with a header.
func PrintStackTablePlain(w io.Writer, stacks []discovery.Stack) {
	tw := newPlainWriter(w)
	fmt.Fprintln(tw, "HOST\tSTACK\tDIR\tCONTAINERS")
	for _, s := range stacks {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", s.Host, s.Name, s.Dir, len(s.Containers))
	}
	tw.Flush()
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
