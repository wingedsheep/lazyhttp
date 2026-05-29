package httpfile

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTree creates path (with any parent dirs) holding content, failing the
// test on error. Unlike writeFile it makes intermediate directories, so a nested
// plan tree can be laid down in one call.
func writeTree(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverPlans(t *testing.T) {
	root := t.TempDir()
	writeTree(t, filepath.Join(root, "root.http"), "GET https://example.com\n")
	writeTree(t, filepath.Join(root, "api", "users.rest"), "GET https://example.com/users\n")
	writeTree(t, filepath.Join(root, "api", "posts.http"), "GET https://example.com/posts\n")
	// Noise that must be ignored: wrong extension, a dot-dir, a dependency cache.
	writeTree(t, filepath.Join(root, "notes.txt"), "not a plan\n")
	writeTree(t, filepath.Join(root, ".hidden", "secret.http"), "GET https://nope\n")
	writeTree(t, filepath.Join(root, "node_modules", "dep.http"), "GET https://nope\n")

	idx := DiscoverPlans(root)
	if idx.Err != nil {
		t.Fatalf("unexpected walk error: %v", idx.Err)
	}

	// Sorted by Rel, only the three real plans, with the right Dir grouping.
	want := []struct{ rel, dir, name string }{
		{"api/posts.http", "api", "posts.http"},
		{"api/users.rest", "api", "users.rest"},
		{"root.http", "", "root.http"},
	}
	if len(idx.Files) != len(want) {
		t.Fatalf("got %d files, want %d: %+v", len(idx.Files), len(want), idx.Files)
	}
	for i, w := range want {
		got := idx.Files[i]
		if got.Rel != w.rel || got.Dir != w.dir || got.Name != w.name {
			t.Errorf("file %d = {Rel:%q Dir:%q Name:%q}, want {Rel:%q Dir:%q Name:%q}",
				i, got.Rel, got.Dir, got.Name, w.rel, w.dir, w.name)
		}
		if !filepath.IsAbs(got.Path) {
			t.Errorf("file %d Path %q is not absolute", i, got.Path)
		}
	}
}

func TestDiscoverPlansEmpty(t *testing.T) {
	idx := DiscoverPlans(t.TempDir())
	if idx.Err != nil {
		t.Fatalf("unexpected error: %v", idx.Err)
	}
	if len(idx.Files) != 0 {
		t.Fatalf("expected no plans, got %+v", idx.Files)
	}
}

func TestCountSteps(t *testing.T) {
	root := t.TempDir()
	plan := filepath.Join(root, "two.http")
	writeTree(t, plan, "GET https://example.com/a\n\n###\n\nGET https://example.com/b\n")
	if n := CountSteps(plan); n != 2 {
		t.Errorf("CountSteps = %d, want 2", n)
	}
	if n := CountSteps(filepath.Join(root, "missing.http")); n != -1 {
		t.Errorf("CountSteps(missing) = %d, want -1", n)
	}
}
