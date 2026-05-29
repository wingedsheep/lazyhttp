package httpfile

import (
	"os"
	"path/filepath"
	"reflect"
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
