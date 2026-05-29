package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// JSON highlight styles (Catppuccin: blue keys, green strings, peach numbers,
// teal literals, dim punctuation).
var (
	jsonKey   = lipgloss.NewStyle().Foreground(palette.blue)
	jsonStr   = lipgloss.NewStyle().Foreground(palette.success)
	jsonNum   = lipgloss.NewStyle().Foreground(palette.warning)
	jsonLit   = lipgloss.NewStyle().Foreground(palette.teal) // true/false/null
	jsonPunct = lipgloss.NewStyle().Foreground(palette.subtle)
)

// highlightJSON colourizes a (pretty-printed) JSON document. If s doesn't look
// like JSON it's returned unchanged, so non-JSON bodies pass through untouched.
func highlightJSON(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return s
	}

	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); {
		switch c := runes[i]; {
		case c == '"':
			tok, n := scanString(runes[i:])
			// A string acting as an object key is followed by a ':'.
			if isKey(runes, i+n) {
				b.WriteString(jsonKey.Render(tok))
			} else {
				b.WriteString(jsonStr.Render(tok))
			}
			i += n
		case c == '-' || (c >= '0' && c <= '9'):
			tok, n := scanNumber(runes[i:])
			b.WriteString(jsonNum.Render(tok))
			i += n
		case strings.HasPrefix(string(runes[i:]), "true"):
			b.WriteString(jsonLit.Render("true"))
			i += 4
		case strings.HasPrefix(string(runes[i:]), "false"):
			b.WriteString(jsonLit.Render("false"))
			i += 5
		case strings.HasPrefix(string(runes[i:]), "null"):
			b.WriteString(jsonLit.Render("null"))
			i += 4
		case strings.ContainsRune("{}[],:", c):
			b.WriteString(jsonPunct.Render(string(c)))
			i++
		default:
			b.WriteRune(c) // whitespace and anything else
			i++
		}
	}
	return b.String()
}

// scanString reads a JSON string starting at runes[0] == '"', honouring escapes,
// and returns the literal (including quotes) and how many runes it spans.
func scanString(runes []rune) (string, int) {
	i := 1
	for i < len(runes) {
		switch runes[i] {
		case '\\':
			i += 2
			continue
		case '"':
			return string(runes[:i+1]), i + 1
		}
		i++
	}
	return string(runes), len(runes) // unterminated; take the rest
}

// scanNumber reads a JSON number and returns it with its rune length.
func scanNumber(runes []rune) (string, int) {
	i := 0
	for i < len(runes) && strings.ContainsRune("+-0123456789.eE", runes[i]) {
		i++
	}
	return string(runes[:i]), i
}

// isKey reports whether the next non-space rune at or after idx is a colon.
func isKey(runes []rune, idx int) bool {
	for i := idx; i < len(runes); i++ {
		if runes[i] == ' ' || runes[i] == '\t' || runes[i] == '\n' || runes[i] == '\r' {
			continue
		}
		return runes[i] == ':'
	}
	return false
}
