package ui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/wingedsheep/lazyhttp/internal/exec"
	"github.com/wingedsheep/lazyhttp/internal/httpfile"
	"github.com/wingedsheep/lazyhttp/internal/runner"
	"github.com/wingedsheep/lazyhttp/internal/step"
)

// TestRender drives the model through a resize and a few navigation keys,
// asserting it renders both panes without panicking.
func TestRender(t *testing.T) {
	path := filepath.Join("..", "..", "example.http")
	m := New(path, "dev")
	if m.loadErr != nil {
		t.Fatalf("load error: %v", m.loadErr)
	}
	if len(m.plan.Steps) == 0 {
		t.Fatal("no steps parsed from example.http")
	}

	var model tea.Model = m
	model, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	// Navigate down and toggle focus; none of this should panic.
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})

	out := model.View()
	for _, want := range []string{"lazyhttp", "STEPS", "RESPONSE"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q", want)
		}
	}
}

// TestRequestPreviewToggle verifies the request preview is hidden by default
// and revealed by `i`.
func TestRequestPreviewToggle(t *testing.T) {
	m := New(filepath.Join("..", "..", "example.http"), "dev")
	var model tea.Model = m
	model, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	// Move the cursor to the "Create product" step (the one with a JSON body).
	const bodyMarker = "price"
	for range indexOfBody(m.plan.Steps, bodyMarker) {
		model, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	}

	if strings.Contains(model.View(), bodyMarker) {
		t.Error("request body should be hidden by default")
	}
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if !strings.Contains(model.View(), bodyMarker) {
		t.Error("request body should appear after pressing i")
	}
}

// indexOfBody returns the index of the first step whose body contains sub.
func indexOfBody(steps []step.Step, sub string) int {
	for i, s := range steps {
		if strings.Contains(s.Body, sub) {
			return i
		}
	}
	return 0
}

// TestEnvPicker verifies E opens the environment picker, the motion keys move
// the highlight, Enter switches the active environment (reloading the plan), and
// Esc dismisses it without changing anything.
func TestEnvPicker(t *testing.T) {
	m := New(filepath.Join("..", "..", "example.http"), "dev")
	if len(m.envNames) < 2 {
		t.Fatalf("expected at least two envs, got %v", m.envNames)
	}
	var model tea.Model = m
	model, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	// Open the picker.
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}})
	if !model.(Model).envPicking {
		t.Fatal("E should open the env picker")
	}
	if !strings.Contains(model.View(), "SELECT ENVIRONMENT") {
		t.Error("picker view should show its title")
	}

	// Esc cancels without switching.
	cancelled, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cancelled.(Model).envPicking {
		t.Error("esc should close the picker")
	}
	if cancelled.(Model).envName != "dev" {
		t.Error("esc should leave the environment unchanged")
	}

	// Re-open, move to the next env, and apply it.
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}})
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})

	got := model.(Model)
	if got.envPicking {
		t.Error("enter should close the picker")
	}
	want := m.envNames[1] // sorted: the entry after "dev"
	if got.envName != want {
		t.Errorf("envName = %q, want %q", got.envName, want)
	}
	if !strings.Contains(got.View(), "env:"+want) {
		t.Errorf("status bar should show the switched env %q", want)
	}
}

// TestEnvPickerNone verifies the picker offers a "(none)" entry that switches to
// no environment (empty inline-only var set), which the status bar then shows
// as "env:(none)" so the current choice stays explicit.
func TestEnvPickerNone(t *testing.T) {
	m := New(filepath.Join("..", "..", "example.http"), "dev")
	var model tea.Model = m
	model, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	// Open the picker (cursor on the current "dev"), move up to "(none)", apply.
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}})
	if !strings.Contains(model.View(), "(none)") {
		t.Error("picker should offer a (none) option")
	}
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})

	got := model.(Model)
	if got.envName != "" {
		t.Errorf("envName = %q, want empty", got.envName)
	}
	if !strings.Contains(got.View(), "env:(none)") {
		t.Error("status bar should show env:(none) when no environment is selected")
	}
}

// TestEnvPickerFitsTerminal guards against the picker rendering taller than the
// terminal, which the renderer can't draw and shows as a frozen/garbled UI. The
// open picker's view must never exceed the window height at any size.
func TestEnvPickerFitsTerminal(t *testing.T) {
	for _, rows := range []int{6, 8, 10, 12, 14, 18, 24, 30, 50} {
		m := New(filepath.Join("..", "..", "example.http"), "dev")
		var model tea.Model = m
		model, _ = model.Update(tea.WindowSizeMsg{Width: 80, Height: rows})
		model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}})
		if got := strings.Count(model.View(), "\n") + 1; got > rows {
			t.Errorf("rows=%d: picker view is %d lines, exceeds terminal", rows, got)
		}
	}
}

// newModel builds a Model wired to the given plan, for the chain/reset wiring
// tests that drive onResult directly (no Bubble Tea harness).
func newModel(p *runner.Plan, runFrom int) Model {
	return Model{plan: p, runFrom: runFrom}
}

// TestRunFromHereChains verifies the run-from-here chain stops on failure.
func TestRunFromHereChains(t *testing.T) {
	m := newModel(&runner.Plan{
		Steps:   []step.Step{{Name: "a"}, {Name: "b"}, {Name: "c"}},
		Results: make([]step.Result, 3),
	}, 0)
	// A successful result at index 0 should request the next step.
	_, cmd := m.onResult(exec.ResultMsg{Index: 0, Result: step.Result{Status: step.Done, StatusCode: 200}})
	if cmd == nil {
		t.Error("expected chain to continue after success")
	}

	// A failing result should halt the chain.
	m.runFrom = 0
	_, cmd = m.onResult(exec.ResultMsg{Index: 0, Result: step.Result{Status: step.Failed, StatusCode: 500}})
	if cmd != nil {
		t.Error("expected chain to stop after failure")
	}
}

// TestFailingAssertionStopsChain verifies a failed assertion marks the step not
// OK and halts a run-from-here chain.
func TestFailingAssertionStopsChain(t *testing.T) {
	m := newModel(&runner.Plan{
		Vars: httpfile.Vars{},
		Steps: []step.Step{
			{Kind: step.KindHTTP, Asserts: []step.Assertion{{Expr: "status", Op: "==", Want: "200"}}},
			{Kind: step.KindHTTP},
		},
		Results: make([]step.Result, 2),
	}, 0)
	// The response is 500, so the status==200 assertion fails.
	_, cmd := m.onResult(exec.ResultMsg{Index: 0, Result: step.Result{
		Status: step.Done, StatusCode: 500, Body: "{}",
	}})
	if m.plan.Results[0].AssertsPass() {
		t.Error("assertion should have failed")
	}
	if m.plan.Results[0].OK() {
		t.Error("step with a failing assertion should not be OK")
	}
	if cmd != nil {
		t.Error("chain should stop when an assertion fails")
	}
}

// TestResetStepClearsState verifies a successful @reset step clears other steps'
// results and drops captured variables, while keeping its own result.
func TestResetStepClearsState(t *testing.T) {
	m := newModel(&runner.Plan{
		Vars:     httpfile.Vars{"host": "http://api", "token": "stale"},
		BaseVars: httpfile.Vars{"host": "http://api"},
		Steps: []step.Step{
			{Kind: step.KindHTTP, Reset: true}, // the clear-DB step
			{Kind: step.KindHTTP},
		},
		Results: []step.Result{
			{Status: step.Pending},
			{Status: step.Done, StatusCode: 200}, // a previously-run step
		},
	}, -1)

	// onResult reassigns the vars map on its returned model, so inspect that.
	updated, _ := m.onResult(exec.ResultMsg{Index: 0, Result: step.Result{Status: step.Done, StatusCode: 200}})
	m = updated.(Model)

	if m.plan.Results[0].Status != step.Done {
		t.Error("the reset step should keep its own result")
	}
	if m.plan.Results[1].Status != step.Pending {
		t.Error("other steps should be reset to pending")
	}
	if _, ok := m.plan.Vars["token"]; ok {
		t.Error("captured variables should be dropped on reset")
	}
	if m.plan.Vars["host"] != "http://api" {
		t.Error("base variables should survive a reset")
	}
}
