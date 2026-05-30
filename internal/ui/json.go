package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// jsonStyles is the palette highlightJSON paints with (Catppuccin: blue keys,
// green strings, peach numbers, teal literals, dim punctuation). It is grouped
// and passed by value so a highlight running off the UI thread can take a
// snapshot of the live theme and never race a theme switch that rebuilds it.
type jsonStyles struct {
	key   lipgloss.Style // object keys
	str   lipgloss.Style // string values
	num   lipgloss.Style // numbers
	lit   lipgloss.Style // true/false/null
	punct lipgloss.Style // structural punctuation
}

// newJSONStyles builds the highlight palette from the active theme.
func newJSONStyles() jsonStyles {
	return jsonStyles{
		key:   lipgloss.NewStyle().Foreground(palette.blue),
		str:   lipgloss.NewStyle().Foreground(palette.success),
		num:   lipgloss.NewStyle().Foreground(palette.warning),
		lit:   lipgloss.NewStyle().Foreground(palette.teal),
		punct: lipgloss.NewStyle().Foreground(palette.subtle),
	}
}

// jsonTheme is the live highlight palette, rebuilt by applyTheme on a theme
// switch. Read it only on the UI thread; off-thread callers (see Model.run)
// must snapshot it into a local first.
var jsonTheme = newJSONStyles()

// reqHighlightCache memoizes the syntax-highlighted form of request-preview
// bodies, keyed by the raw body text. The preview re-renders on every cursor
// move and resize while it's toggled on (`i`); without this each render re-runs
// the JSON highlighter over an unchanged body — the response body is already
// cached in Model.bodyView, the request body was not. It is held by pointer so
// the value-receiver render methods can populate it, and is dropped on a theme
// switch — the one thing that changes the colours a given body highlights to. A
// body that changes (a capture feeding it) is simply a new key, so the cache
// self-invalidates without explicit clearing.
type reqHighlightCache struct {
	entries map[string]string
}

func newReqHighlightCache() *reqHighlightCache {
	return &reqHighlightCache{entries: map[string]string{}}
}

// render returns body highlighted, reusing a prior result for the same text.
func (c *reqHighlightCache) render(body string) string {
	if v, ok := c.entries[body]; ok {
		return v
	}
	v := highlightJSON(body, jsonTheme)
	c.entries[body] = v
	return v
}

// highlightJSON colourizes a (pretty-printed) JSON document with st. If s
// doesn't look like JSON it's returned unchanged, so non-JSON bodies (e.g. shell
// output) pass through untouched.
func highlightJSON(s string, st jsonStyles) string {
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
				b.WriteString(st.key.Render(tok))
			} else {
				b.WriteString(st.str.Render(tok))
			}
			i += n
		case c == '-' || (c >= '0' && c <= '9'):
			tok, n := scanNumber(runes[i:])
			b.WriteString(st.num.Render(tok))
			i += n
		case hasRunePrefix(runes, i, "true"):
			b.WriteString(st.lit.Render("true"))
			i += 4
		case hasRunePrefix(runes, i, "false"):
			b.WriteString(st.lit.Render("false"))
			i += 5
		case hasRunePrefix(runes, i, "null"):
			b.WriteString(st.lit.Render("null"))
			i += 4
		case strings.ContainsRune("{}[],:", c):
			b.WriteString(st.punct.Render(string(c)))
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

// hasRunePrefix reports whether word appears at runes[idx:] without allocating.
// (string(runes[idx:]) would copy the entire remaining document on every call,
// turning the highlighter quadratic on large bodies.)
func hasRunePrefix(runes []rune, idx int, word string) bool {
	for _, w := range word {
		if idx >= len(runes) || runes[idx] != w {
			return false
		}
		idx++
	}
	return true
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
