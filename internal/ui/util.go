package ui

import (
	"regexp"

	"github.com/charmbracelet/lipgloss"
)

// ansiPattern matches SGR colour escape sequences, stripped when copying the
// response pane to the clipboard so the user gets clean text, not terminal codes.
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// stripANSI removes colour escapes from s.
func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

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
	if lipgloss.Width(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	out := []rune(s)
	for lipgloss.Width(string(out)) > w-1 && len(out) > 0 {
		out = out[:len(out)-1]
	}
	return string(out) + "…"
}
