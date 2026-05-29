package ui

import "github.com/charmbracelet/bubbles/key"

// keyMap defines every binding. k9s users will feel at home: hjkl-ish motion,
// g/G to jump, ctrl+d/u for half-page leaps.
type keyMap struct {
	Up       key.Binding
	Down     key.Binding
	Top      key.Binding
	Bottom   key.Binding
	HalfUp   key.Binding
	HalfDn   key.Binding
	Run      key.Binding
	RunAll   key.Binding
	Focus    key.Binding
	Reload   key.Binding
	Clear    key.Binding
	ClearAll key.Binding
	Request  key.Binding
	Filter   key.Binding
	Theme    key.Binding
	Env      key.Binding
	Copy     key.Binding
	CopyAll  key.Binding
	Back     key.Binding
	Help     key.Binding
	Quit     key.Binding

	// folderMode is set when the plan was opened from the folder overview; it
	// surfaces the `:files` "back to overview" hint, which is meaningless when a
	// single file was opened directly. The `:` command itself is handled by App,
	// not this keymap — Back is documentation only.
	folderMode bool
}

func newKeyMap() keyMap {
	return keyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Top:      key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "first")),
		Bottom:   key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "last")),
		HalfUp:   key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("^u", "half-page up")),
		HalfDn:   key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("^d", "half-page down")),
		Run:      key.NewBinding(key.WithKeys("enter", "e"), key.WithHelp("enter", "run")),
		RunAll:   key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "run from here")),
		Focus:    key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "focus")),
		Reload:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reload")),
		Clear:    key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "clear")),
		ClearAll: key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "clear all")),
		Request:  key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "toggle details")),
		Filter:   key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Theme:    key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "theme")),
		Env:      key.NewBinding(key.WithKeys("E"), key.WithHelp("E", "switch env")),
		Copy:     key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy body")),
		CopyAll:  key.NewBinding(key.WithKeys("Y"), key.WithHelp("Y", "copy response pane")),
		Back:     key.NewBinding(key.WithKeys(":"), key.WithHelp(":files", "overview")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// ShortHelp is the one-line footer hint set (satisfies help.KeyMap). It stays
// deliberately lean so it fits a narrow terminal; the rest lives behind `?`. In
// folder mode the `:files` back-to-overview hint is included.
func (k keyMap) ShortHelp() []key.Binding {
	hints := []key.Binding{k.Run, k.RunAll, k.Filter, k.Focus, k.Reload}
	if k.folderMode {
		hints = append(hints, k.Back)
	}
	return append(hints, k.Help, k.Quit)
}

// FullHelp is the expanded ? overlay, grouped into columns (satisfies help.KeyMap).
func (k keyMap) FullHelp() [][]key.Binding {
	last := []key.Binding{k.Request, k.Filter, k.Theme, k.Env, k.Copy, k.CopyAll}
	if k.folderMode {
		last = append(last, k.Back)
	}
	last = append(last, k.Focus, k.Help, k.Quit)
	return [][]key.Binding{
		{k.Up, k.Down, k.Top, k.Bottom, k.HalfUp, k.HalfDn},
		{k.Run, k.RunAll, k.Clear, k.ClearAll, k.Reload},
		last,
	}
}
