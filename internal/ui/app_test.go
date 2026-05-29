package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/wingedsheep/lazyhttp/internal/httpfile"
	"github.com/wingedsheep/lazyhttp/internal/step"
)

// writePlan creates a .http file (with parent dirs) under root.
func writePlan(t *testing.T, root, rel, body string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// typeRunes feeds each rune of s to the model as a key press.
func typeRunes(t *testing.T, model tea.Model, s string) tea.Model {
	t.Helper()
	for _, r := range s {
		model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return model
}

// press sends a key and, if the update returns a command (e.g. the browser's
// "open" emits openPlanMsg via a tea.Cmd), runs it and feeds the resulting
// message back — mirroring what the Bubble Tea runtime does.
func press(t *testing.T, model tea.Model, key tea.KeyMsg) tea.Model {
	t.Helper()
	model, cmd := model.Update(key)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			model, _ = model.Update(msg)
		}
	}
	return model
}

// TestBrowserListsPlans checks the overview lists discovered plans (grouped by
// subfolder) and reports the count in the status bar.
func TestBrowserListsPlans(t *testing.T) {
	root := t.TempDir()
	writePlan(t, root, "root.http", "GET https://example.com\n")
	writePlan(t, root, "api/users.rest", "GET https://example.com/users\n")

	var model tea.Model = NewApp(root, "")
	model, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	out := ansiRe.ReplaceAllString(model.View(), "")
	for _, want := range []string{"PLANS", "root.http", "users.rest", "api"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "1/2") {
		t.Errorf("overview status bar missing 1/2 position count:\n%s", out)
	}
}

// TestOpenPlanAndReturn drives the core navigation: enter opens the selected
// plan, and `:files` returns to the overview.
func TestOpenPlanAndReturn(t *testing.T) {
	root := t.TempDir()
	writePlan(t, root, "root.http", "### Hello\nGET https://example.com\n")

	var model tea.Model = NewApp(root, "")
	model, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	// Enter opens the plan under the cursor; the plan view shows the step list.
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if app := model.(App); !app.showPlan {
		t.Fatal("expected plan view foreground after enter")
	}
	if out := ansiRe.ReplaceAllString(model.View(), ""); !strings.Contains(out, "STEPS") {
		t.Fatalf("plan view missing STEPS header:\n%s", out)
	}

	// `:files` returns to the overview.
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	model = typeRunes(t, model, "files")
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if app := model.(App); app.showPlan {
		t.Fatal("expected overview foreground after :files")
	}
	if out := ansiRe.ReplaceAllString(model.View(), ""); !strings.Contains(out, "PLANS") {
		t.Fatalf("overview not shown after :files:\n%s", out)
	}
}

// TestUnknownCommandKeepsBarOpen checks an unrecognised `:` command reports an
// error and keeps the bar open rather than silently closing.
func TestUnknownCommandKeepsBarOpen(t *testing.T) {
	root := t.TempDir()
	writePlan(t, root, "root.http", "GET https://example.com\n")

	var model tea.Model = NewApp(root, "")
	model, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // open plan
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	model = typeRunes(t, model, "nope")
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})

	app := model.(App)
	if !app.cmdActive {
		t.Error("expected command bar to stay open after unknown command")
	}
	if app.cmdErr == "" {
		t.Error("expected an error message for unknown command")
	}
}

// TestBrowserFilter checks the `/` filter narrows the list to matching paths.
func TestBrowserFilter(t *testing.T) {
	root := t.TempDir()
	writePlan(t, root, "users.http", "GET https://example.com/users\n")
	writePlan(t, root, "orders.http", "GET https://example.com/orders\n")

	var model tea.Model = NewApp(root, "")
	model, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = typeRunes(t, model, "order")

	out := ansiRe.ReplaceAllString(model.View(), "")
	if !strings.Contains(out, "orders.http") || strings.Contains(out, "users.http") {
		t.Errorf("filter did not narrow to orders.http:\n%s", out)
	}
}

// TestEmptyFolderNotice checks an empty folder explains itself instead of a
// blank pane.
func TestEmptyFolderNotice(t *testing.T) {
	idx := httpfile.DiscoverPlans(t.TempDir())
	b := newBrowser(idx)
	b.width, b.height = 100, 30
	b.layout()
	if out := ansiRe.ReplaceAllString(b.View(), ""); !strings.Contains(out, "no .http or .rest files") {
		t.Errorf("expected empty-folder notice:\n%s", out)
	}
}

// TestEscReturnsToOverview checks Esc pops from an open plan back to the overview
// when the plan has no use for the key.
func TestEscReturnsToOverview(t *testing.T) {
	root := t.TempDir()
	writePlan(t, root, "p.http", "GET https://example.com\n")

	var model tea.Model = NewApp(root, "")
	model, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // open
	if !model.(App).showPlan {
		t.Fatal("expected plan view after enter")
	}
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if model.(App).showPlan {
		t.Fatal("Esc should return to the overview")
	}
}

// TestEscClearsFilterBeforeLeaving checks Esc first clears an applied filter in
// the plan, and only a second Esc pops to the overview.
func TestEscClearsFilterBeforeLeaving(t *testing.T) {
	root := t.TempDir()
	writePlan(t, root, "p.http", "GET https://example.com\n")

	var model tea.Model = NewApp(root, "")
	model, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // open

	// Apply a filter inside the plan: `/`, type, Enter.
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = typeRunes(t, model, "zzz")
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})

	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc}) // clears filter, stays in plan
	if !model.(App).showPlan {
		t.Fatal("first Esc should clear the filter, not leave the plan")
	}
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc}) // now pops up
	if model.(App).showPlan {
		t.Fatal("second Esc should return to the overview")
	}
}

// TestUnresolvedVarFailsStep checks running a step with a missing {{var}} fails
// with a clear message instead of firing a request with literal braces.
func TestUnresolvedVarFailsStep(t *testing.T) {
	root := t.TempDir()
	p := writePlan(t, root, "p.http", "GET {{api}}/users\n")

	var model tea.Model = New(p, "")
	model, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // run step 0

	r := model.(Model).plan.Results[0]
	if r.Status != step.Failed || r.Err == nil || !strings.Contains(r.Err.Error(), "unresolved") {
		t.Fatalf("expected an unresolved-variable failure, got status=%v err=%v", r.Status, r.Err)
	}
}

// TestCopyNotRunNotice checks copying a step that hasn't run reports a notice
// rather than copying nothing.
func TestCopyNotRunNotice(t *testing.T) {
	root := t.TempDir()
	p := writePlan(t, root, "p.http", "GET https://example.com\n")

	var model tea.Model = New(p, "")
	model, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = press(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	if n := model.(Model).notice; !strings.Contains(n, "nothing to copy") {
		t.Fatalf("expected a 'nothing to copy' notice, got %q", n)
	}
}

// TestCopiedNoticeSuccess checks a finished copy shows a green confirmation.
func TestCopiedNoticeSuccess(t *testing.T) {
	root := t.TempDir()
	p := writePlan(t, root, "p.http", "GET https://example.com\n")

	var model tea.Model = New(p, "")
	model, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model, _ = model.Update(copiedMsg{label: "response body (3 B)"})

	m := model.(Model)
	if !m.noticeOK || !strings.Contains(m.notice, "copied") {
		t.Fatalf("expected a success copy notice, got ok=%v notice=%q", m.noticeOK, m.notice)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int]string{0: "0 B", 512: "512 B", 2048: "2.0 KB", 3 << 20: "3.0 MB"}
	for n, want := range cases {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
