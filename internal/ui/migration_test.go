package ui

import (
	"bytes"
	"context"
	"image/color"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/wingedsheep/lazyhttp/internal/step"
)

func migrationModel(t *testing.T) Model {
	t.Helper()
	m := New(filepath.Join("..", "..", "example.http"), "dev")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return updated.(Model)
}

func TestV2Input(t *testing.T) {
	m := migrationModel(t)
	updated, _ := m.Update(tea.KeyReleaseMsg{Code: tea.KeyDown})
	if updated.(Model).cursor != m.cursor {
		t.Fatal("key release moved cursor")
	}
	for range wheelStep {
		updated, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		m = updated.(Model)
	}
	if m.cursor != 1 {
		t.Fatalf("wheel cursor = %d", m.cursor)
	}
	// The result pane begins directly after the rendered list's border.
	updated, _ = m.Update(tea.MouseClickMsg{X: m.listW + 2, Y: 4, Button: tea.MouseLeft})
	m = updated.(Model)
	if m.focus != focusResult {
		t.Fatal("click did not focus response")
	}
	m.plan.Results[m.cursor] = step.Result{Status: step.Done, StatusCode: 200, Body: strings.Repeat("line\n", 100)}
	m.refreshResult()
	updated, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	scrolled := updated.(Model)
	if scrolled.viewport.YOffset() == 0 {
		t.Fatal("response wheel did not scroll")
	}
	m.filtering = true
	updated, _ = m.Update(tea.PasteMsg{Content: "hello\n世界"})
	m = updated.(Model)
	if m.filter != "hello 世界" {
		t.Fatalf("paste = %q", m.filter)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if updated.(Model).filter != "hello 世" {
		t.Fatal("backspace did not remove one rune")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c did not quit in filter")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c returned a non-quit command")
	}
}

func TestV2BackgroundAndTerminalModes(t *testing.T) {
	previous, dark := activeTheme, darkBackground
	t.Cleanup(func() { setBackground(dark); applyTheme(previous) })
	SetTheme("Catppuccin")
	m := migrationModel(t)
	before := m.styles.logo.Render("logo")
	updated, _ := m.Update(tea.BackgroundColorMsg{Color: color.White})
	m = updated.(Model)
	if darkBackground || m.styles.logo.Render("logo") == before {
		t.Fatal("light background did not update styles")
	}
	if v := m.View(); !v.AltScreen || v.MouseMode != tea.MouseModeCellMotion {
		t.Fatal("missing terminal modes")
	}
	// Background replies also arrive while an unreadable plan is displayed.
	broken := New(filepath.Join(t.TempDir(), "missing.http"), "")
	if _, cmd := broken.Update(tea.BackgroundColorMsg{Color: color.Black}); cmd != nil {
		t.Fatal("background update should not schedule work")
	}
	a := NewApp(t.TempDir(), "")
	for _, h := range []int{10, 24, 40} {
		next, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: h})
		a = next.(App)
		v := a.View()
		if !v.AltScreen || v.MouseMode != tea.MouseModeCellMotion || lipgloss.Height(v.Content) > h || lipgloss.Width(v.Content) > 80 {
			t.Fatal("folder view dimensions or terminal modes incorrect")
		}
	}
	a.cmdActive = true
	next, _ := a.Update(tea.PasteMsg{Content: "files\n"})
	a = next.(App)
	if a.cmdInput != "files " {
		t.Fatalf("command paste = %q", a.cmdInput)
	}
	if !a.View().AltScreen {
		t.Fatal("command bar lost alternate screen")
	}
	a.cmdActive = false
	a.browser.filtering = true
	next, _ = a.Update(tea.PasteMsg{Content: "test"})
	if next.(App).browser.filter != "test" {
		t.Fatal("folder filter lost paste")
	}
}

func TestV2StyledText(t *testing.T) {
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Render("hello 世界")
	got := truncate(styled, 8)
	if lipgloss.Width(got) > 8 || strings.Contains(stripANSI(got), "\x1b") || !strings.HasSuffix(stripANSI(got), "…") {
		t.Fatalf("invalid styled truncation: %q", got)
	}
}

func TestV2ProgramLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out bytes.Buffer
	p := tea.NewProgram(migrationModel(t), tea.WithContext(ctx), tea.WithInput(strings.NewReader("q")), tea.WithOutput(&out), tea.WithWindowSize(120, 30), tea.WithoutSignalHandler())
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\x1b[?1049h") || !strings.Contains(out.String(), "\x1b[?1049l") {
		t.Fatal("program did not enter and restore alternate screen")
	}
}
