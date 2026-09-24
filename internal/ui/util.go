package ui

import "github.com/charmbracelet/x/ansi"

// stripANSI removes terminal escapes before copying styled output.
func stripANSI(s string) string { return ansi.Strip(s) }

// clamp constrains v to the inclusive range [lo, hi].
func clamp(v, lo, hi int) int {
	return max(lo, min(v, hi))
}

// truncate shortens s to a printable width of w, appending an ellipsis. It is
// width-aware so styled/wide runes don't break the layout.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}
