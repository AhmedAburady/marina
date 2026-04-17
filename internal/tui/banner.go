package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// marinaBanner is the block-ASCII logo rendered on the Home screen. It is the
// verbatim output of `figlet -f ANSI-shadow "MARINA"` (leading spaces from
// the figlet -c centering flag stripped; the Home screen centres the block
// itself based on terminal width). All six rows share the same visual width
// so the horizontal gradient (see renderBanner) lines up cleanly.
var marinaBanner = []string{
	"███╗   ███╗ █████╗ ██████╗ ██╗███╗   ██╗ █████╗ ",
	"████╗ ████║██╔══██╗██╔══██╗██║████╗  ██║██╔══██╗",
	"██╔████╔██║███████║██████╔╝██║██╔██╗ ██║███████║",
	"██║╚██╔╝██║██╔══██║██╔══██╗██║██║╚██╗██║██╔══██║",
	"██║ ╚═╝ ██║██║  ██║██║  ██║██║██║ ╚████║██║  ██║",
	"╚═╝     ╚═╝╚═╝  ╚═╝╚═╝  ╚═╝╚═╝╚═╝  ╚═══╝╚═╝  ╚═╝",
}

// bannerGradient is a full rainbow — red → orange → yellow → green → cyan →
// purple → pink — picked to visually match the `lolcat -p 1` output the
// banner was piped through on the command line. Seven stops so the gradient
// has enough resolution to keep each letter multi-hued rather than flat.
var bannerGradient = []string{
	"#ff6b6b", // red / coral
	"#ffa657", // orange
	"#f1fa8c", // yellow
	"#50fa7b", // green
	"#38BDF8", // cyan
	"#7D56F4", // purple
	"#EC4899", // pink
}

// renderBanner draws the Marina logo with a horizontal gradient, each cell
// rendered on the panel background so the banner blends seamlessly into the
// Home screen body.
func renderBanner() []string {
	if len(marinaBanner) == 0 {
		return nil
	}
	maxW := 0
	for _, l := range marinaBanner {
		if w := len([]rune(l)); w > maxW {
			maxW = w
		}
	}

	out := make([]string, len(marinaBanner))
	for i, line := range marinaBanner {
		var b strings.Builder
		for j, r := range []rune(line) {
			t := 0.0
			if maxW > 1 {
				t = float64(j) / float64(maxW-1)
			}
			hex := gradientAt(bannerGradient, t)
			// Paint each cell on the app background so the banner blends
			// into the home screen — no grey box effect around it.
			cell := lipgloss.NewStyle().
				Background(cBg).
				Foreground(lipgloss.Color(hex)).
				Render(string(r))
			b.WriteString(cell)
		}
		out[i] = b.String()
	}
	return out
}

// gradientAt interpolates the palette at t in [0,1] and returns the hex code.
func gradientAt(palette []string, t float64) string {
	if len(palette) == 0 {
		return "#ffffff"
	}
	if len(palette) == 1 || t <= 0 {
		return palette[0]
	}
	if t >= 1 {
		return palette[len(palette)-1]
	}
	segments := len(palette) - 1
	idx := int(t * float64(segments))
	if idx >= segments {
		idx = segments - 1
	}
	local := t*float64(segments) - float64(idx)
	return lerpHex(palette[idx], palette[idx+1], local)
}

// lerpHex blends two #RRGGBB strings by `t` in [0,1] and returns a #RRGGBB.
func lerpHex(a, b string, t float64) string {
	ra, ga, ba := hexRGB(a)
	rb, gb, bb := hexRGB(b)
	r := int(float64(ra) + t*float64(rb-ra))
	g := int(float64(ga) + t*float64(gb-ga))
	bl := int(float64(ba) + t*float64(bb-ba))
	return toHex(r, g, bl)
}

func hexRGB(s string) (r, g, b int) {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return 0, 0, 0
	}
	return parseHexByte(s[0:2]), parseHexByte(s[2:4]), parseHexByte(s[4:6])
}

func parseHexByte(s string) int {
	v := 0
	for _, c := range s {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= int(c - '0')
		case c >= 'a' && c <= 'f':
			v |= int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= int(c-'A') + 10
		}
	}
	return v
}

func toHex(r, g, b int) string {
	clamp := func(v int) int {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return v
	}
	const digits = "0123456789abcdef"
	r, g, b = clamp(r), clamp(g), clamp(b)
	out := []byte{'#', 0, 0, 0, 0, 0, 0}
	out[1] = digits[r/16]
	out[2] = digits[r%16]
	out[3] = digits[g/16]
	out[4] = digits[g%16]
	out[5] = digits[b/16]
	out[6] = digits[b%16]
	return string(out)
}

// bannerWidth returns the visual width (in cells) of one banner line — used
// by Home to centre the banner inside the panel.
func bannerWidth() int {
	if len(marinaBanner) == 0 {
		return 0
	}
	return len([]rune(marinaBanner[0]))
}
