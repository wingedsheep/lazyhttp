package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// theme groups every colour the UI uses, so retheming is a one-stop edit.
// Every colour is an AdaptiveColor pair — Catppuccin Mocha on dark terminals,
// Catppuccin Latte on light ones — so the UI stays legible either way.
type theme struct {
	accent  lipgloss.AdaptiveColor // mauve  — focus, cursor, headers
	blue    lipgloss.AdaptiveColor // GET requests / JSON keys
	teal    lipgloss.AdaptiveColor // shell steps / JSON literals
	success lipgloss.AdaptiveColor // green  — 2xx, passing checks, POST
	warning lipgloss.AdaptiveColor // peach  — 3xx, PUT/PATCH, numbers
	danger  lipgloss.AdaptiveColor // red    — 4xx/5xx, failures, DELETE
	fg      lipgloss.AdaptiveColor // primary text
	subtle  lipgloss.AdaptiveColor // dim / secondary text
	border  lipgloss.AdaptiveColor // unfocused pane borders
	selBg   lipgloss.AdaptiveColor // selected-row background
	crust   lipgloss.AdaptiveColor // status-bar background
}

// adaptive is a tiny constructor so a palette reads as Dark/Light pairs.
func adaptive(dark, light string) lipgloss.AdaptiveColor {
	return lipgloss.AdaptiveColor{Dark: dark, Light: light}
}

// mono builds a palette whose colours don't adapt to the terminal background;
// the themes designed for a single (dark) scheme use it so every field is one
// value. Arguments are in the same order as the theme struct fields.
func mono(accent, blue, teal, success, warning, danger, fg, subtle, border, selBg, crust string) theme {
	m := func(c string) lipgloss.AdaptiveColor { return lipgloss.AdaptiveColor{Dark: c, Light: c} }
	return theme{
		accent: m(accent), blue: m(blue), teal: m(teal), success: m(success),
		warning: m(warning), danger: m(danger), fg: m(fg), subtle: m(subtle),
		border: m(border), selBg: m(selBg), crust: m(crust),
	}
}

// catppuccin pairs Catppuccin Mocha (dark) with Catppuccin Latte (light) — the
// only theme that adapts to the terminal background.
var catppuccin = theme{
	accent:  adaptive("#cba6f7", "#8839ef"), // mauve
	blue:    adaptive("#89b4fa", "#1e66f5"),
	teal:    adaptive("#94e2d5", "#179299"),
	success: adaptive("#a6e3a1", "#40a02b"), // green
	warning: adaptive("#fab387", "#fe640b"), // peach
	danger:  adaptive("#f38ba8", "#d20f39"), // red
	fg:      adaptive("#cdd6f4", "#4c4f69"), // text
	subtle:  adaptive("#7f849c", "#8c8fa1"), // overlay1
	border:  adaptive("#45475a", "#bcc0cc"), // surface1
	selBg:   adaptive("#313244", "#ccd0da"), // surface0
	crust:   adaptive("#181825", "#dce0e8"), // mantle / crust
}

//                    accent     blue       teal       success    warning    danger     fg         subtle     border     selBg      crust
var dracula = mono("#bd93f9", "#8be9fd", "#ff79c6", "#50fa7b", "#ffb86c", "#ff5555", "#f8f8f2", "#6272a4", "#44475a", "#44475a", "#21222c")
var nord = mono("#b48ead", "#81a1c1", "#8fbcbb", "#a3be8c", "#d08770", "#bf616a", "#eceff4", "#616e88", "#434c5e", "#3b4252", "#2e3440")
var gruvbox = mono("#d3869b", "#83a598", "#8ec07c", "#b8bb26", "#fe8019", "#fb4934", "#ebdbb2", "#928374", "#504945", "#3c3836", "#1d2021")
var tokyoNight = mono("#bb9af7", "#7aa2f7", "#7dcfff", "#9ece6a", "#ff9e64", "#f7768e", "#c0caf5", "#565f89", "#3b4261", "#283457", "#16161e")

// namedTheme pairs a display name with its palette.
type namedTheme struct {
	name    string
	palette theme
}

// themes is the ordered set the theme key cycles through; the first entry is
// the startup default.
var themes = []namedTheme{
	{"Catppuccin", catppuccin},
	{"Dracula", dracula},
	{"Nord", nord},
	{"Gruvbox", gruvbox},
	{"Tokyo Night", tokyoNight},
}

// activeTheme indexes the live entry in themes; palette mirrors its colours.
var activeTheme int

// palette is the active colour set, read throughout rendering. It is swapped at
// runtime by applyTheme — callers holding palette-derived styles (Model.styles,
// the spinner, help, the cached body views) must rebuild them; see
// Model.cycleTheme.
var palette = themes[0].palette

// applyTheme makes themes[i] (wrapping out-of-range indices) the active palette
// and rebuilds the package-level JSON highlight styles derived from it.
func applyTheme(i int) {
	activeTheme = ((i % len(themes)) + len(themes)) % len(themes)
	palette = themes[activeTheme].palette
	jsonKey = lipgloss.NewStyle().Foreground(palette.blue)
	jsonStr = lipgloss.NewStyle().Foreground(palette.success)
	jsonNum = lipgloss.NewStyle().Foreground(palette.warning)
	jsonLit = lipgloss.NewStyle().Foreground(palette.teal)
	jsonPunct = lipgloss.NewStyle().Foreground(palette.subtle)
}

// ActiveThemeName returns the name of the live theme, so callers can persist
// the user's choice across launches.
func ActiveThemeName() string {
	return themes[activeTheme].name
}

// ThemeNames returns the selectable theme names in cycle order, for help text.
func ThemeNames() []string {
	names := make([]string, len(themes))
	for i, t := range themes {
		names[i] = t.name
	}
	return names
}

// SetTheme switches to the named theme (case-insensitive), reporting whether
// the name matched. Call it before New so the initial styles pick up the choice.
func SetTheme(name string) bool {
	for i, t := range themes {
		if strings.EqualFold(t.name, name) {
			applyTheme(i)
			return true
		}
	}
	return false
}

// styles holds the reusable lipgloss styles. Only the static ones live here;
// per-row colours are composed on the fly (see cell).
type styles struct {
	logo       lipgloss.Style // the "lazyhttp" badge in the status bar
	barText    lipgloss.Style // status-bar text segments
	paneHeader lipgloss.Style // STEPS / RESPONSE / ASSERTIONS headers
	dim        lipgloss.Style
	method     lipgloss.Style
	group      lipgloss.Style
}

func newStyles() styles {
	return styles{
		logo: lipgloss.NewStyle().
			Bold(true).Foreground(palette.crust).Background(palette.accent).
			Padding(0, 1),
		barText: lipgloss.NewStyle().
			Foreground(palette.subtle).Background(palette.crust),
		paneHeader: lipgloss.NewStyle().
			Bold(true).Foreground(palette.accent).
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(palette.border),
		dim:    lipgloss.NewStyle().Foreground(palette.subtle),
		method: lipgloss.NewStyle().Bold(true),
		group: lipgloss.NewStyle().
			Foreground(palette.accent).Bold(true),
	}
}

// cell builds a per-row segment style. When sel is true it paints the selected
// background so a whole highlighted row stays gap-free across coloured segments.
func cell(fg lipgloss.TerminalColor, sel, bold bool) lipgloss.Style {
	s := lipgloss.NewStyle().Foreground(fg)
	if bold {
		s = s.Bold(true)
	}
	if sel {
		s = s.Background(palette.selBg)
	}
	return s
}

// pane returns a bordered container whose border colour signals focus.
func pane(focused bool, w, h int) lipgloss.Style {
	border := palette.border
	if focused {
		border = palette.accent
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Width(w).Height(h).
		Padding(0, 1)
}

// methodColor maps an HTTP verb to its badge colour (httpie-style).
func methodColor(method string) lipgloss.TerminalColor {
	switch strings.ToUpper(method) {
	case "GET", "HEAD":
		return palette.blue
	case "POST":
		return palette.success
	case "PUT", "PATCH":
		return palette.warning
	case "DELETE":
		return palette.danger
	default:
		return palette.teal
	}
}

// statusColor maps an HTTP status code (or shell exit code) to a colour.
func statusColor(code int, isHTTP bool) lipgloss.TerminalColor {
	if isHTTP {
		switch {
		case code >= 200 && code < 300:
			return palette.success
		case code >= 300 && code < 400:
			return palette.warning
		default:
			return palette.danger
		}
	}
	if code == 0 {
		return palette.success
	}
	return palette.danger
}
