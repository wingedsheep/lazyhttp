package httpfile

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestLoadEnvNames verifies the environment names are read from the env file
// next to the plan and returned sorted.
func TestLoadEnvNames(t *testing.T) {
	dir := t.TempDir()
	env := `{"prod": {"host": "h"}, "dev": {"host": "h"}, "staging": {"host": "h"}}`
	if err := os.WriteFile(filepath.Join(dir, "http-client.env.json"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadEnvNames(filepath.Join(dir, "plan.http"))
	if err != nil {
		t.Fatalf("LoadEnvNames: %v", err)
	}
	want := []string{"dev", "prod", "staging"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestLoadEnvNamesMissingFile verifies a missing env file yields no names and no
// error, so a plan without environments simply has none to pick.
func TestLoadEnvNamesMissingFile(t *testing.T) {
	got, err := LoadEnvNames(filepath.Join(t.TempDir(), "plan.http"))
	if err != nil {
		t.Fatalf("LoadEnvNames: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

// TestLoadEnvNamesAncestor verifies the env file is discovered by walking up
// from the plan's directory: a shared http-client.env.json at a common root is
// found by a plan nested in a subfolder, matching IntelliJ/VS Code.
func TestLoadEnvNamesAncestor(t *testing.T) {
	root := t.TempDir()
	env := `{"prod": {"host": "h"}, "dev": {"host": "h"}}`
	if err := os.WriteFile(filepath.Join(root, "http-client.env.json"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "feature-x", "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := LoadEnvNames(filepath.Join(sub, "plan.http"))
	if err != nil {
		t.Fatalf("LoadEnvNames: %v", err)
	}
	want := []string{"dev", "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestLoadEnvNamesRelativePath verifies discovery works for a bare relative plan
// path (the common `cd <dir> && lazyhttp plan.http` invocation): the walk must
// absolutize against the cwd before climbing, or filepath.Dir("plan.http") == "."
// dead-ends it on the first iteration and no ancestor env file is found.
func TestLoadEnvNamesRelativePath(t *testing.T) {
	root := t.TempDir()
	env := `{"ecc-test": {"host": "h"}}`
	if err := os.WriteFile(filepath.Join(root, "http-client.env.json"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "feature")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	got, err := LoadEnvNames("plan.http")
	if err != nil {
		t.Fatalf("LoadEnvNames: %v", err)
	}
	if want := []string{"ecc-test"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (relative path must be absolutized before walking)", got, want)
	}
}

// TestLoadEnvNamesClosestWins verifies the walk uses the nearest env file: a
// subfolder's own http-client.env.json shadows an ancestor's rather than merging.
func TestLoadEnvNamesClosestWins(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "http-client.env.json"), []byte(`{"shared": {"host": "h"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "feature-x")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "http-client.env.json"), []byte(`{"local": {"host": "h"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadEnvNames(filepath.Join(sub, "plan.http"))
	if err != nil {
		t.Fatalf("LoadEnvNames: %v", err)
	}
	if want := []string{"local"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (closest file should win)", got, want)
	}
}

// TestLoadEnvNamesGitBoundary verifies the walk stops at the repo root: an env
// file above a .git directory is invisible, so the search can't escape the
// project into unrelated parents.
func TestLoadEnvNamesGitBoundary(t *testing.T) {
	root := t.TempDir()
	// Env file lives ABOVE the repo boundary and must not be found.
	if err := os.WriteFile(filepath.Join(root, "http-client.env.json"), []byte(`{"outside": {"host": "h"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	sub := filepath.Join(repo, "http", "feature-x")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := LoadEnvNames(filepath.Join(sub, "plan.http"))
	if err != nil {
		t.Fatalf("LoadEnvNames: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want none (walk must stop at the .git boundary)", got)
	}
}

// TestLoadEnvPrivateOverlay verifies the private env file layers over the shared
// one per-variable: a secret defined only in http-client.private.env.json
// resolves, a shared value the private file overrides takes the private value,
// and shared values the private file doesn't mention are untouched.
func TestLoadEnvPrivateOverlay(t *testing.T) {
	dir := t.TempDir()
	shared := `{"dev": {"host": "https://api.dev", "token": "shared-token"}}`
	private := `{"dev": {"token": "real-secret", "clientSecret": "s3cr3t"}}`
	if err := os.WriteFile(filepath.Join(dir, "http-client.env.json"), []byte(shared), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "http-client.private.env.json"), []byte(private), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadEnv(filepath.Join(dir, "plan.http"), "dev")
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	want := Vars{"host": "https://api.dev", "token": "real-secret", "clientSecret": "s3cr3t"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoadEnv = %v, want %v", got, want)
	}
}

// TestLoadEnvPrivateSecretWithSharedAuth verifies the canonical split: the
// Security.Auth block lives in the shared file referencing {{clientSecret}}, the
// secret itself lives in the private file. LoadAuth still finds the Security
// block and LoadEnv supplies the secret to expand it.
func TestLoadEnvPrivateSecretWithSharedAuth(t *testing.T) {
	dir := t.TempDir()
	shared := `{"dev": {"Security": {"Auth": {"api": {"Client Secret": "{{clientSecret}}"}}}}}`
	private := `{"dev": {"clientSecret": "s3cr3t"}}`
	if err := os.WriteFile(filepath.Join(dir, "http-client.env.json"), []byte(shared), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "http-client.private.env.json"), []byte(private), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(dir, "plan.http")

	vars, err := LoadEnv(plan, "dev")
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	if vars["clientSecret"] != "s3cr3t" {
		t.Errorf("clientSecret = %q, want %q", vars["clientSecret"], "s3cr3t")
	}
	auths, err := LoadAuth(plan, "dev")
	if err != nil {
		t.Fatalf("LoadAuth: %v", err)
	}
	if got := auths["api"].ClientSecret; got != "{{clientSecret}}" {
		t.Errorf("ClientSecret placeholder = %q, want it preserved for expansion", got)
	}
}

// TestLoadEnvNamesPrivateOnly verifies a private env file is discovered and its
// environments offered even when no shared file sits beside it.
func TestLoadEnvNamesPrivateOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "http-client.private.env.json"), []byte(`{"local": {"host": "h"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadEnvNames(filepath.Join(dir, "plan.http"))
	if err != nil {
		t.Fatalf("LoadEnvNames: %v", err)
	}
	if want := []string{"local"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestDiscoverEnvFound verifies discovery reports the names, the file that
// supplied them, and that Summary stays empty when environments resolved.
func TestDiscoverEnvFound(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "http-client.env.json")
	if err := os.WriteFile(file, []byte(`{"prod": {"host": "h"}, "dev": {"host": "h"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d := DiscoverEnv(filepath.Join(dir, "plan.http"))
	if want := []string{"dev", "prod"}; !reflect.DeepEqual(d.Names, want) {
		t.Errorf("Names = %v, want %v", d.Names, want)
	}
	if d.File != file {
		t.Errorf("File = %q, want %q", d.File, file)
	}
	if s := d.Summary(); s != "" {
		t.Errorf("Summary = %q, want empty when envs were found", s)
	}
}

// TestDiscoverEnvMissing verifies a missing file is not an error, leaves File
// empty, and produces a Summary naming the directories searched.
func TestDiscoverEnvMissing(t *testing.T) {
	dir := t.TempDir()
	d := DiscoverEnv(filepath.Join(dir, "plan.http"))
	if d.Err != nil {
		t.Fatalf("Err = %v, want nil for a missing file", d.Err)
	}
	if len(d.Names) != 0 || d.File != "" {
		t.Errorf("Names=%v File=%q, want empty", d.Names, d.File)
	}
	if len(d.Searched) == 0 {
		t.Fatal("Searched is empty, want the directories walked")
	}
	if s := d.Summary(); !strings.Contains(s, "no environments") || !strings.Contains(s, "http-client.env.json") {
		t.Errorf("Summary = %q, want a 'no environments … http-client.env.json' diagnostic", s)
	}
}

// TestDiscoverEnvParseError verifies a malformed file is reported in Err (not
// discarded), with Summary surfacing it rather than a misleading "not found".
func TestDiscoverEnvParseError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "http-client.env.json"), []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	d := DiscoverEnv(filepath.Join(dir, "plan.http"))
	if d.Err == nil {
		t.Fatal("Err = nil, want a parse error")
	}
	if s := d.Summary(); !strings.Contains(s, "env file error") {
		t.Errorf("Summary = %q, want it to surface the parse error", s)
	}
}

// TestDiscoverEnvEmptyFile verifies a well-formed file declaring no environments
// is distinguished from a missing one: Summary names the file rather than the
// search path.
func TestDiscoverEnvEmptyFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "http-client.env.json")
	if err := os.WriteFile(file, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d := DiscoverEnv(filepath.Join(dir, "plan.http"))
	if len(d.Names) != 0 {
		t.Errorf("Names = %v, want none", d.Names)
	}
	if s := d.Summary(); !strings.Contains(s, "declared in") || !strings.Contains(s, file) {
		t.Errorf("Summary = %q, want it to name the empty file %q", s, file)
	}
}

// TestExpandFuncResolver verifies ExpandFunc consults the resolver first and,
// when it declines (or is nil), falls back to the variable map — leaving unknown
// placeholders untouched. The widened matcher must also carry JSON-path
// punctuation ($ . [ ]) inside a token through to the resolver intact.
func TestExpandFuncResolver(t *testing.T) {
	v := Vars{"host": "example.com"}
	resolve := func(token string) (string, bool) {
		if token == "login.response.body.$.items[0].id" {
			return "42", true
		}
		return "", false // decline everything else
	}

	got := v.ExpandFunc("{{host}}/o/{{login.response.body.$.items[0].id}}/{{missing}}", resolve)
	want := "example.com/o/42/{{missing}}"
	if got != want {
		t.Errorf("ExpandFunc = %q, want %q", got, want)
	}

	// A nil resolver behaves exactly like Expand: the reference is unknown and
	// stays literal rather than erroring.
	if got := v.ExpandFunc("{{login.response.body.$.id}}", nil); got != "{{login.response.body.$.id}}" {
		t.Errorf("nil resolver = %q, want the placeholder untouched", got)
	}
}

// TestExpandNested verifies a variable whose value references another variable
// expands transitively — the composed-variable convention of IntelliJ HTTP
// Client and VS Code REST Client.
func TestExpandNested(t *testing.T) {
	v := Vars{
		"host":    "https://api.dev",
		"baseUrl": "{{host}}/v2",
		"orders":  "{{baseUrl}}/orders",
	}
	if got, want := v.Expand("{{orders}}"), "https://api.dev/v2/orders"; got != want {
		t.Errorf("Expand = %q, want %q", got, want)
	}
}

// TestExpandCycle verifies a self-referential definition is left literal after
// the chain folds back on itself, rather than looping forever.
func TestExpandCycle(t *testing.T) {
	v := Vars{"a": "{{b}}", "b": "{{a}}"}
	// {{a}} → {{b}} → {{a}}; the second {{a}} is on the chain, so it stops.
	if got, want := v.Expand("{{a}}"), "{{a}}"; got != want {
		t.Errorf("Expand = %q, want %q", got, want)
	}
}

// TestExpandDynamicOnce verifies dynamic and resolver-provided values are
// terminal: a resolved value that itself contains literal "{{...}}" (e.g.
// captured response data) is inserted verbatim, not re-expanded.
func TestExpandDynamicOnce(t *testing.T) {
	v := Vars{"host": "example.com"}
	resolve := func(token string) (string, bool) {
		if token == "captured" {
			return "{{host}}", true // response data that looks like a placeholder
		}
		return "", false
	}
	if got, want := v.ExpandFunc("{{captured}}", resolve), "{{host}}"; got != want {
		t.Errorf("ExpandFunc = %q, want %q (response data must not re-expand)", got, want)
	}
}
