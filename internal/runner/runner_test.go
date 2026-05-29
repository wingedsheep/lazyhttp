package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/wingedsheep/lazyhttp/internal/auth"
	"github.com/wingedsheep/lazyhttp/internal/httpfile"
	"github.com/wingedsheep/lazyhttp/internal/step"
)

// TestExpandBodyFromFile verifies a `< file` body is read relative to the plan
// directory and sent verbatim, while a `<@ file` body has its {{vars}} expanded.
func TestExpandBodyFromFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "body.json"), []byte(`{"name":"{{who}}"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &Plan{Dir: dir, Vars: httpfile.Vars{"who": "ada"}}

	// `<` sends the file verbatim — placeholders stay raw.
	plain, err := p.Expand(step.Step{Kind: step.KindHTTP, BodyFile: "body.json"})
	if err != nil {
		t.Fatalf("expand plain: %v", err)
	}
	if plain.Body != `{"name":"{{who}}"}` {
		t.Errorf("plain body = %q, want raw placeholder", plain.Body)
	}

	// `<@` expands {{vars}} in the file contents.
	expanded, err := p.Expand(step.Step{Kind: step.KindHTTP, BodyFile: "body.json", BodyFileVars: true})
	if err != nil {
		t.Fatalf("expand vars: %v", err)
	}
	if expanded.Body != `{"name":"ada"}` {
		t.Errorf("expanded body = %q, want vars resolved", expanded.Body)
	}

	// A missing file surfaces as an error so the step can fail visibly.
	if _, err := p.Expand(step.Step{Kind: step.KindHTTP, BodyFile: "nope.json"}); err == nil {
		t.Error("expected an error for a missing body file")
	}
}

// TestCaptureFlowsIntoLaterStep verifies a value captured from one response is
// stored in the var set and expanded into a subsequent step's request.
func TestCaptureFlowsIntoLaterStep(t *testing.T) {
	p := &Plan{
		Vars: httpfile.Vars{"host": "http://api"},
		Steps: []step.Step{
			{
				Kind:     step.KindHTTP,
				Method:   "POST",
				URL:      "{{host}}/posts",
				Captures: []step.Capture{{Name: "postId", Expr: "json.id"}},
			},
			{Kind: step.KindHTTP, Method: "GET", URL: "{{host}}/posts/{{postId}}"},
		},
		Results: make([]step.Result, 2),
	}

	// Step 0 returns an id; evaluating its result should populate vars["postId"].
	p.Results[0] = p.Evaluate(0, step.Result{Status: step.Done, StatusCode: 201, Body: `{"id": 42}`})
	if p.Vars["postId"] != "42" {
		t.Fatalf("capture not stored, vars = %v", p.Vars)
	}

	// Step 1's request should now expand using the captured value.
	expanded, err := p.Expand(p.Steps[1])
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if expanded.URL != "http://api/posts/42" {
		t.Errorf("expanded URL = %q, want http://api/posts/42", expanded.URL)
	}
}

// TestResponseRefFlowsIntoLaterStep verifies inline response references
// ({{name.response.body.$.path}}, {{name.response.headers.X}}) resolve against an
// earlier named step's stored result, the way VS Code REST Client plans expect.
func TestResponseRefFlowsIntoLaterStep(t *testing.T) {
	hdr := http.Header{}
	hdr.Set("Location", "/sessions/9")
	p := &Plan{
		Vars: httpfile.Vars{"host": "http://api"},
		Steps: []step.Step{
			{Name: "login", Kind: step.KindHTTP, Method: "POST", URL: "{{host}}/login"},
			{
				Kind:    step.KindHTTP,
				Method:  "GET",
				URL:     "{{host}}/me/{{login.response.body.$.id}}",
				Headers: map[string]string{"Authorization": "Bearer {{login.response.body.token}}"},
				Body:    "loc={{login.response.headers.Location}}",
			},
		},
		Results: []step.Result{
			{Status: step.Done, StatusCode: 200, Body: `{"token":"abc","id":7}`, Header: hdr},
			{},
		},
	}

	got, err := p.Expand(p.Steps[1])
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
	if got, _ := p.Expand(unrun); got.URL != "{{nope.response.body.$.id}}" {
		t.Errorf("unresolved ref = %q, want the placeholder untouched", got.URL)
	}
}

// TestAuthResolver verifies a resolver is built only for steps that reference
// {{$auth.token}}, that config values (here a {{var}} client secret) are
// expanded against the var set, and that the resolver fetches and attaches a
// token end-to-end.
func TestAuthResolver(t *testing.T) {
	var gotSecret string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotSecret = r.PostForm.Get("client_secret")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"ABC","expires_in":3600}`))
	}))
	defer srv.Close()

	p := &Plan{
		Vars:      httpfile.Vars{"secret": "sssh"},
		AuthCache: auth.NewCache(),
		AuthConfigs: map[string]auth.Config{
			"demo": {
				GrantType:         "Client Credentials",
				TokenURL:          srv.URL,
				ClientID:          "id",
				ClientSecret:      "{{secret}}", // expanded against the var set
				ClientCredentials: "in body",
			},
		},
	}

	// A step with no auth reference gets no resolver.
	if r := p.AuthResolver(step.Step{URL: "http://x", Headers: map[string]string{}}); r != nil {
		t.Error("expected nil resolver for a step without an $auth reference")
	}

	// A step that references the token gets a resolver that fetches and attaches it.
	s := step.Step{
		Kind:    step.KindHTTP,
		URL:     "http://x",
		Headers: map[string]string{"Authorization": `Bearer {{$auth.token("demo")}}`},
	}
	r := p.AuthResolver(s)
	if r == nil {
		t.Fatal("expected a resolver for a step referencing $auth.token")
	}
	if err := r.Resolve(&s); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if s.Headers["Authorization"] != "Bearer ABC" {
		t.Errorf("token not attached, header = %q", s.Headers["Authorization"])
	}
	if gotSecret != "sssh" {
		t.Errorf("client secret not expanded, server saw %q", gotSecret)
	}
}

// TestRunAll runs a multi-step plan end to end, verifying captures thread
// forward into a later request, assertions are evaluated, and a failed
// assertion stops the chain so subsequent steps never run.
func TestRunAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":7}`))
		case "/b/7":
			w.WriteHeader(http.StatusOK)
		case "/fail":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	pass := []step.Assertion{{Expr: "status", Op: "==", Want: "200"}}
	p := &Plan{
		Vars: httpfile.Vars{"host": srv.URL},
		Steps: []step.Step{
			{Kind: step.KindHTTP, Method: "GET", URL: "{{host}}/a",
				Captures: []step.Capture{{Name: "id", Expr: "json.id"}}, Asserts: pass},
			{Kind: step.KindHTTP, Method: "GET", URL: "{{host}}/b/{{id}}", Asserts: pass},
			{Kind: step.KindHTTP, Method: "GET", URL: "{{host}}/fail", Asserts: pass},
			{Kind: step.KindHTTP, Method: "GET", URL: "{{host}}/a"}, // must not run
		},
		Results: make([]step.Result, 4),
	}

	results, err := p.RunAll(context.Background())
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if !results[0].OK() {
		t.Errorf("step 0 should pass, got %+v", results[0])
	}
	if p.Vars["id"] != "7" {
		t.Errorf("capture not threaded forward, vars = %v", p.Vars)
	}
	if !results[1].OK() {
		t.Errorf("step 1 should pass with the captured id expanded, got %+v", results[1])
	}
	if results[2].OK() {
		t.Error("step 2 (500) should fail its status==200 assertion")
	}
	if results[3].Status != step.Pending {
		t.Error("step 3 should never run once the chain stops on a failure")
	}
}

// TestRunAllReset verifies a successful @reset step mid-run clears the earlier
// step's result and drops captured variables back to the baseline, while keeping
// its own result.
func TestRunAllReset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/login" {
			w.Write([]byte(`{"token":"t1"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := &Plan{
		Vars:     httpfile.Vars{"host": srv.URL},
		BaseVars: httpfile.Vars{"host": srv.URL},
		Steps: []step.Step{
			{Name: "login", Kind: step.KindHTTP, Method: "POST", URL: "{{host}}/login",
				Captures: []step.Capture{{Name: "token", Expr: "json.token"}}},
			{Kind: step.KindHTTP, Method: "POST", URL: "{{host}}/reset", Reset: true},
		},
		Results: make([]step.Result, 2),
	}

	if _, err := p.RunAll(context.Background()); err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if _, ok := p.Vars["token"]; ok {
		t.Error("the reset step should drop captured variables")
	}
	if p.Results[0].Status != step.Pending {
		t.Error("the pre-reset step's result should be cleared")
	}
	if p.Results[1].Status != step.Done {
		t.Error("the reset step should keep its own result")
	}
}
