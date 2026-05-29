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
