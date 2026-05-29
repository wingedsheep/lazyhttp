package exec

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
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

	cmd := Run(0, step.Step{Kind: step.KindHTTP, Method: "POST", URL: srv.URL, Body: "{}"}, nil, nil)
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

// stubResolver is a minimal AuthResolver: it appends a fixed token to the
// Authorization header so the test can confirm Run consults the resolver before
// building the request, and that an error fails the step.
type stubResolver struct{ err error }

func (s stubResolver) Resolve(st *step.Step) error {
	if s.err != nil {
		return s.err
	}
	st.Headers["Authorization"] = "Bearer resolved"
	return nil
}

// TestRunHTTPAuth verifies Run resolves auth before sending: the server sees the
// resolved Authorization header, not the placeholder.
func TestRunHTTPAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := step.Step{Kind: step.KindHTTP, Method: "GET", URL: srv.URL,
		Headers: map[string]string{"Authorization": `Bearer {{$auth.token("x")}}`}}
	msg := Run(0, s, stubResolver{}, nil)().(ResultMsg)
	if msg.Result.Err != nil {
		t.Fatalf("unexpected error: %v", msg.Result.Err)
	}
	if gotAuth != "Bearer resolved" {
		t.Errorf("server saw Authorization %q, want resolved token", gotAuth)
	}
}

// TestRunHTTPAuthError verifies a resolver error fails the step (no request is
// sent unauthenticated).
func TestRunHTTPAuthError(t *testing.T) {
	s := step.Step{Kind: step.KindHTTP, Method: "GET", URL: "http://127.0.0.1:0",
		Headers: map[string]string{}}
	msg := Run(0, s, stubResolver{err: errTest}, nil)().(ResultMsg)
	if msg.Result.Status != step.Failed || msg.Result.Err == nil {
		t.Errorf("expected a failed step on auth error, got %+v", msg.Result)
	}
}

var errTest = &authErr{}

type authErr struct{}

func (*authErr) Error() string { return "token fetch failed" }

// TestRunShell exercises the shell runner and exit-code capture. The body is
// written per-OS because shell steps run through the native shell (PowerShell
// on Windows, $SHELL/sh elsewhere) and aren't portable; output is compared
// loosely (trimmed/contains) so trailing-newline conventions don't matter.
func TestRunShell(t *testing.T) {
	body := "echo hello && exit 3"
	if runtime.GOOS == "windows" {
		body = "echo hello; exit 3"
	}

	cmd := Run(1, step.Step{Kind: step.KindShell, Body: body}, nil, nil)
	msg := cmd().(ResultMsg)
	if msg.Result.ExitCode != 3 {
		t.Errorf("exit code: want 3, got %d", msg.Result.ExitCode)
	}
	if msg.Result.OK() {
		t.Error("non-zero exit should not be OK")
	}
	if got := strings.TrimSpace(msg.Result.Body); got != "hello" {
		t.Errorf("shell output: want %q, got %q", "hello", got)
	}
}
