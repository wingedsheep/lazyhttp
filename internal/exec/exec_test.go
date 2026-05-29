package exec

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

// TestRunHTTP exercises the full HTTP runner against a local server and checks
// the ResultMsg it produces.
func TestRunHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":42}`))
	}))
	defer srv.Close()

	cmd := Run(0, step.Step{Kind: step.KindHTTP, Method: "POST", URL: srv.URL, Body: "{}"}, nil)
	msg, ok := cmd().(ResultMsg)
	if !ok {
		t.Fatalf("expected ResultMsg, got %T", cmd())
	}
	if msg.Result.StatusCode != http.StatusCreated {
		t.Errorf("status: want 201, got %d", msg.Result.StatusCode)
	}
	if !msg.Result.OK() {
		t.Errorf("expected OK result")
	}
	// JSON body should be pretty-printed (indented).
	if msg.Result.Body != "{\n  \"id\": 42\n}" {
		t.Errorf("body not pretty-printed: %q", msg.Result.Body)
	}
}

// TestRunShell exercises the shell runner and exit-code capture.
func TestRunShell(t *testing.T) {
	cmd := Run(1, step.Step{Kind: step.KindShell, Body: "echo hello && exit 3"}, nil)
	msg := cmd().(ResultMsg)
	if msg.Result.ExitCode != 3 {
		t.Errorf("exit code: want 3, got %d", msg.Result.ExitCode)
	}
	if msg.Result.OK() {
		t.Error("non-zero exit should not be OK")
	}
	if got := msg.Result.Body; got != "hello\n" {
		t.Errorf("shell output: want %q, got %q", "hello\n", got)
	}
}
