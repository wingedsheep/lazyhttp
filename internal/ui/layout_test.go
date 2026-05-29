package ui

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/wingedsheep/lazyhttp/internal/exec"
	"github.com/wingedsheep/lazyhttp/internal/step"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// TestLayoutFitsWidth renders the UI with finished results at several terminal
// widths and asserts no rendered line is wider than the terminal — a guard
// against the pane/viewport width math drifting and causing wrapping.
func TestLayoutFitsWidth(t *testing.T) {
	for _, w := range []int{80, 100, 140} {
		m := New(filepath.Join("..", "..", "example.http"), "dev")
		var model tea.Model = m
		model, _ = model.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		model, _ = model.Update(exec.ResultMsg{Index: 0, Result: step.Result{
			Status: step.Done, StatusCode: 200, Body: `{"id":42,"ok":true,"name":"Ada"}`,
		}})
		model, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})

		stripped := ansiRe.ReplaceAllString(model.View(), "")
		for n, line := range strings.Split(stripped, "\n") {
			if got := len([]rune(line)); got > w {
				t.Errorf("width %d: line %d is %d cols (wraps): %q", w, n, got, line)
			}
		}
	}
}

// TestLayoutFitsHeight renders the UI at several terminal sizes and asserts the
// View is never taller than the terminal. In the alt-screen an over-tall frame
// scrolls the top line off — which silently hid the status bar (file/env/filter/
// theme). The result pane's two-row header was the off-by-one culprit.
func TestLayoutFitsHeight(t *testing.T) {
	for _, h := range []int{10, 16, 24, 30, 50} {
		m := New(filepath.Join("..", "..", "example.http"), "dev")
		var model tea.Model = m
		model, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: h})
		model, _ = model.Update(exec.ResultMsg{Index: 0, Result: step.Result{
			Status: step.Done, StatusCode: 200, Body: `{"id":42,"ok":true,"name":"Ada"}`,
		}})

		if got := strings.Count(model.View(), "\n") + 1; got > h {
			t.Errorf("height %d: View is %d lines, exceeds terminal (status bar scrolls off)", h, got)
		}
		// The status bar must survive in the rendered frame.
		if !strings.Contains(model.View(), "lazyhttp") {
			t.Errorf("height %d: status bar (lazyhttp) missing from View", h)
		}
	}
}

// TestRunningRowWidth guards the spinner glyph column: a running step's row must
// render to exactly the list's inner width, like every other row. spinner.Dot's
// trailing-space frames are width 2 and once overflowed the fixed glyph cell,
// wrapping the row onto a second line and bumping it downward.
func TestRunningRowWidth(t *testing.T) {
	for _, w := range []int{80, 100, 140} {
		m := New(filepath.Join("..", "..", "example.http"), "dev")
		var model tea.Model = m
		model, _ = model.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		// Mark step 0 running, then advance the spinner so a real frame renders.
		model, _ = model.Update(exec.ResultMsg{Index: 0, Result: step.Result{Status: step.Running}})
		model, _ = model.Update(spinner.TickMsg{})

		run := model.(Model)
		innerW := max(run.listW-2, 8)
		// A grouped step renders with a width-1 tree connector (see renderList).
		row := run.renderRow(0, "├", innerW)
		if got := lipgloss.Width(row); got != innerW {
			t.Errorf("width %d: running row is %d cols, want %d (overflow wraps the row)", w, got, innerW)
		}
	}
}

// TestSpinnerIdle verifies the UI performs no work when nothing is running:
// Init issues no command, and a stray spinner tick does not re-arm the loop.
func TestSpinnerIdle(t *testing.T) {
	m := New(filepath.Join("..", "..", "example.http"), "dev")
	if m.Init() != nil {
		t.Error("Init should be idle (nil cmd) when no step is running")
	}
	var model tea.Model = m
	model, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if _, cmd := model.Update(spinner.TickMsg{}); cmd != nil {
		t.Error("an idle spinner tick should not schedule another tick")
	}
}
