package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePlan writes a .http file in a temp dir with {{host}} pointing at srvURL,
// defined inline so no env file is needed, and returns its path.
func writePlan(t *testing.T, srvURL, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.http")
	content := "@host = " + srvURL + "\n\n" + body
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunCommandExitCodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":1}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	tests := []struct {
		name string
		plan string
		want int
	}{
		{
			name: "all pass",
			plan: "# @assert status == 200\nGET {{host}}/ok\n",
			want: 0,
		},
		{
			// The CI-critical case: the HTTP call succeeds (200) but an assertion
			// fails, so the run must still exit non-zero.
			name: "failed assert on 2xx",
			plan: "# @assert status == 201\nGET {{host}}/ok\n",
			want: 1,
		},
		{
			name: "non-2xx status",
			plan: "GET {{host}}/boom\n",
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writePlan(t, srv.URL, tt.plan)
			got := runCommand([]string{path}, io.Discard, io.Discard)
			if got != tt.want {
				t.Errorf("exit code = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestRunCommandUsageErrors covers the exit-2 paths: a missing argument and an
// unreadable plan file.
func TestRunCommandUsageErrors(t *testing.T) {
	if got := runCommand(nil, io.Discard, io.Discard); got != 2 {
		t.Errorf("no plan argument: exit = %d, want 2", got)
	}
	if got := runCommand([]string{"/no/such/plan.http"}, io.Discard, io.Discard); got != 2 {
		t.Errorf("unreadable plan: exit = %d, want 2", got)
	}
}

// TestRunCommandOutputFormats checks the --output flag: an unknown format is a
// usage error (exit 2), and `-o json` on a failing plan still exits 1 while
// emitting a report whose top-level ok is false.
func TestRunCommandOutputFormats(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	path := writePlan(t, srv.URL, "# @assert status == 201\nGET {{host}}/ok\n")

	if got := runCommand([]string{"-o", "yaml", path}, io.Discard, io.Discard); got != 2 {
		t.Errorf("unknown format: exit = %d, want 2", got)
	}

	var out strings.Builder
	got := runCommand([]string{"-o", "json", path}, &out, io.Discard)
	if got != 1 {
		t.Fatalf("failing plan: exit = %d, want 1", got)
	}
	if !strings.Contains(out.String(), `"ok": false`) {
		t.Errorf("json report should report ok=false:\n%s", out.String())
	}
}

// TestRunCommandFilter verifies --filter runs only matching steps and that the
// summary reports the rest as not run.
func TestRunCommandFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	plan := "" +
		"# @name keep-me\nGET {{host}}/a\n\n" +
		"###\n# @name drop-me\nGET {{host}}/b\n"
	path := writePlan(t, srv.URL, plan)

	var out strings.Builder
	got := runCommand([]string{"--filter", "keep", path}, &out, io.Discard)
	if got != 0 {
		t.Fatalf("exit = %d, want 0", got)
	}
	if !strings.Contains(out.String(), "keep-me") {
		t.Errorf("matching step not reported:\n%s", out.String())
	}
	if strings.Contains(out.String(), "drop-me") {
		t.Errorf("filtered-out step should not be reported:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "1 passed, 0 failed") {
		t.Errorf("summary should count only the run step:\n%s", out.String())
	}
}
