package ui

import "github.com/charmbracelet/bubbles/key"

// keyMap defines every binding. Arrows move (↑/↓ within a pane, ←/→ switch
// panes), g/G jump to the ends, ctrl+d/u leap a half-page.
type keyMap struct {
	Up       key.Binding
	Down     key.Binding
	Left     key.Binding
	Right    key.Binding
	Top      key.Binding
	Bottom   key.Binding
	HalfUp   key.Binding
	HalfDn   key.Binding
	Run      key.Binding
	RunAll   key.Binding
	RunBlock key.Binding
	Stop     key.Binding
	Focus    key.Binding
	Reload   key.Binding
	Clear    key.Binding
	ClearAll key.Binding
	Request  key.Binding
	Headers  key.Binding
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

	// requestOn / headersOn mirror the model's showRequest / showHeaders toggles
	// so the full-help overlay can mark the active ones with a ✓; the model syncs
	// them whenever the toggle flips.
	requestOn bool
	headersOn bool
}

// withState returns b with a "✓" appended to its description when on, so a
// toggle's current state shows in the help overlay.
func withState(b key.Binding, on bool) key.Binding {
	h := b.Help()
	if on {
		return key.NewBinding(key.WithKeys(b.Keys()...), key.WithHelp(h.Key, h.Desc+" ✓"))
	}
	return b
}

func newKeyMap() keyMap {
	return keyMap{
		Up:       key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up")),
		Down:     key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down")),
		Left:     key.NewBinding(key.WithKeys("left"), key.WithHelp("←", "plan")),
		Right:    key.NewBinding(key.WithKeys("right"), key.WithHelp("→", "output")),
		Top:      key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "first")),
		Bottom:   key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "last")),
		HalfUp:   key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("^u", "half-page up")),
		HalfDn:   key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("^d", "half-page down")),
		Run:      key.NewBinding(key.WithKeys("enter", "e"), key.WithHelp("enter", "run")),
		RunAll:   key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "run from here")),
		RunBlock: key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "run block")),
		Stop:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "stop stream")),
		Focus:    key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "focus")),
		Reload:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reload")),
		Clear:    key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "clear")),
		ClearAll: key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "clear all")),
		Request:  key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "request preview")),
		Headers:  key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "response headers")),
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
	last := []key.Binding{withState(k.Request, k.requestOn), withState(k.Headers, k.headersOn), k.Filter, k.Theme, k.Env, k.Copy, k.CopyAll}
	if k.folderMode {
		last = append(last, k.Back)
	}
	last = append(last, k.Help, k.Quit)
	return [][]key.Binding{
		{k.Up, k.Down, k.Left, k.Right, k.Top, k.Bottom, k.HalfUp, k.HalfDn},
		{k.Run, k.RunAll, k.RunBlock, k.Stop, k.Clear, k.ClearAll, k.Reload},
		last,
	}
}
