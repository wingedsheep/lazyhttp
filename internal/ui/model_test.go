package ui

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/wingedsheep/lazyhttp/internal/exec"
	"github.com/wingedsheep/lazyhttp/internal/httpfile"
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
	if len(m.steps) == 0 {
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
	for range indexOfBody(m.steps, bodyMarker) {
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

// TestRunFromHereChains verifies the run-from-here chain stops on failure.
func TestRunFromHereChains(t *testing.T) {
	m := Model{
		steps:   []step.Step{{Name: "a"}, {Name: "b"}, {Name: "c"}},
		results: make([]step.Result, 3),
		runFrom: 0,
	}
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

// TestCaptureFlowsIntoLaterStep verifies a value captured from one response is
// expanded into a subsequent step's request.
func TestCaptureFlowsIntoLaterStep(t *testing.T) {
	m := Model{
		vars: httpfile.Vars{"host": "http://api"},
		steps: []step.Step{
			{
				Kind:     step.KindHTTP,
				Method:   "POST",
				URL:      "{{host}}/posts",
				Captures: []step.Capture{{Name: "postId", Expr: "json.id"}},
			},
			{Kind: step.KindHTTP, Method: "GET", URL: "{{host}}/posts/{{postId}}"},
		},
		results: make([]step.Result, 2),
	}

	// Step 0 returns an id; the capture should populate vars["postId"].
	m.onResult(exec.ResultMsg{Index: 0, Result: step.Result{
		Status: step.Done, StatusCode: 201, Body: `{"id": 42}`,
	}})
	if m.vars["postId"] != "42" {
		t.Fatalf("capture not stored, vars = %v", m.vars)
	}

	// Step 1's request should now expand using the captured value.
	expanded, err := m.expand(m.steps[1])
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	got := expanded.URL
	if got != "http://api/posts/42" {
		t.Errorf("expanded URL = %q, want http://api/posts/42", got)
	}
}

// TestExpandBodyFromFile verifies a `< file` body is read relative to the plan
// directory and sent verbatim, while a `<@ file` body has its {{vars}} expanded.
func TestExpandBodyFromFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "body.json"), []byte(`{"name":"{{who}}"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m := Model{
		path: filepath.Join(dir, "plan.http"),
		vars: httpfile.Vars{"who": "ada"},
	}

	// `<` sends the file verbatim — placeholders stay raw.
	plain, err := m.expand(step.Step{Kind: step.KindHTTP, BodyFile: "body.json"})
	if err != nil {
		t.Fatalf("expand plain: %v", err)
	}
	if plain.Body != `{"name":"{{who}}"}` {
		t.Errorf("plain body = %q, want raw placeholder", plain.Body)
	}

	// `<@` expands {{vars}} in the file contents.
	expanded, err := m.expand(step.Step{Kind: step.KindHTTP, BodyFile: "body.json", BodyFileVars: true})
	if err != nil {
		t.Fatalf("expand vars: %v", err)
	}
	if expanded.Body != `{"name":"ada"}` {
		t.Errorf("expanded body = %q, want vars resolved", expanded.Body)
	}

	// A missing file surfaces as an error so the step can fail visibly.
	if _, err := m.expand(step.Step{Kind: step.KindHTTP, BodyFile: "nope.json"}); err == nil {
		t.Error("expected an error for a missing body file")
	}
}

// TestResponseRefFlowsIntoLaterStep verifies inline response references
// ({{name.response.body.$.path}}, {{name.response.headers.X}}) resolve against an
// earlier named step's stored result, the way VS Code REST Client plans expect.
func TestResponseRefFlowsIntoLaterStep(t *testing.T) {
	m := Model{
		vars: httpfile.Vars{"host": "http://api"},
		steps: []step.Step{
			{Name: "login", Kind: step.KindHTTP, Method: "POST", URL: "{{host}}/login"},
			{
				Kind:    step.KindHTTP,
				Method:  "GET",
				URL:     "{{host}}/me/{{login.response.body.$.id}}",
				Headers: map[string]string{"Authorization": "Bearer {{login.response.body.token}}"},
				Body:    "loc={{login.response.headers.Location}}",
			},
		},
		results: make([]step.Result, 2),
	}

	// The login response carries a token, an id, and a Location header.
	hdr := http.Header{}
	hdr.Set("Location", "/sessions/9")
	m.onResult(exec.ResultMsg{Index: 0, Result: step.Result{
		Status: step.Done, StatusCode: 200,
		Body:   `{"token":"abc","id":7}`,
		Header: hdr,
	}})

	got, err := m.expand(m.steps[1])
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if got.URL != "http://api/me/7" {
		t.Errorf("URL = %q, want http://api/me/7", got.URL)
	}
	if got.Headers["Authorization"] != "Bearer abc" {
		t.Errorf("Authorization = %q, want Bearer abc", got.Headers["Authorization"])
	}
	if got.Body != "loc=/sessions/9" {
		t.Errorf("Body = %q, want loc=/sessions/9", got.Body)
	}

	// A reference to a step that hasn't run stays literal rather than erroring.
	unrun := step.Step{Kind: step.KindHTTP, URL: "{{nope.response.body.$.id}}"}
	if got, _ := m.expand(unrun); got.URL != "{{nope.response.body.$.id}}" {
		t.Errorf("unresolved ref = %q, want the placeholder untouched", got.URL)
	}
}

// TestFailingAssertionStopsChain verifies a failed assertion marks the step not
// OK and halts a run-from-here chain.
func TestFailingAssertionStopsChain(t *testing.T) {
	m := Model{
		vars: httpfile.Vars{},
		steps: []step.Step{
			{Kind: step.KindHTTP, Asserts: []step.Assertion{{Expr: "status", Op: "==", Want: "200"}}},
			{Kind: step.KindHTTP},
		},
		results: make([]step.Result, 2),
		runFrom: 0,
	}
	// The response is 500, so the status==200 assertion fails.
	_, cmd := m.onResult(exec.ResultMsg{Index: 0, Result: step.Result{
		Status: step.Done, StatusCode: 500, Body: "{}",
	}})
	if m.results[0].AssertsPass() {
		t.Error("assertion should have failed")
	}
	if m.results[0].OK() {
		t.Error("step with a failing assertion should not be OK")
	}
	if cmd != nil {
		t.Error("chain should stop when an assertion fails")
	}
}

// TestResetStepClearsState verifies a successful @reset step clears other steps'
// results and drops captured variables, while keeping its own result.
func TestResetStepClearsState(t *testing.T) {
	m := Model{
		vars:     httpfile.Vars{"host": "http://api", "token": "stale"},
		baseVars: httpfile.Vars{"host": "http://api"},
		steps: []step.Step{
			{Kind: step.KindHTTP, Reset: true}, // the clear-DB step
			{Kind: step.KindHTTP},
		},
		results: []step.Result{
			{Status: step.Pending},
			{Status: step.Done, StatusCode: 200}, // a previously-run step
		},
	}

	// onResult reassigns the vars map on its returned model, so inspect that.
	updated, _ := m.onResult(exec.ResultMsg{Index: 0, Result: step.Result{Status: step.Done, StatusCode: 200}})
	m = updated.(Model)

	if m.results[0].Status != step.Done {
		t.Error("the reset step should keep its own result")
	}
	if m.results[1].Status != step.Pending {
		t.Error("other steps should be reset to pending")
	}
	if _, ok := m.vars["token"]; ok {
		t.Error("captured variables should be dropped on reset")
	}
	if m.vars["host"] != "http://api" {
		t.Error("base variables should survive a reset")
	}
}
