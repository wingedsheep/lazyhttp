package main

import (
	"encoding/json"
	"encoding/xml"
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

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestRunCommandReportWriteFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	path := writePlan(t, srv.URL, "GET {{host}}\n")
	for _, format := range []string{"pretty", "json", "junit"} {
		t.Run(format, func(t *testing.T) {
			var diagnostic strings.Builder
			if got := runCommand([]string{"--quiet", "-o", format, path}, failedWriter{}, &diagnostic); got != 1 {
				t.Fatalf("exit = %d, want 1", got)
			}
			if !strings.Contains(diagnostic.String(), "write report:") {
				t.Fatalf("missing write diagnostic: %s", diagnostic.String())
			}
		})
	}
}

func TestRunCommandPreservesHistoryAcrossReset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"next":"changed"}`)
	}))
	defer srv.Close()
	path := writePlan(t, srv.URL, `@target = original
###
# @name Request {{target}}
# @capture target = json.next
# @assert status == 200
GET {{host}}/{{target}}
###
# @name Reset
# @reset
POST {{host}}/reset
###
# @name Fail
# @assert status == 201
GET {{host}}/{{target}}
###
# @name Skipped
GET {{host}}/skipped
`)
	for _, format := range []string{"json", "junit", "pretty"} {
		t.Run(format, func(t *testing.T) {
			var out strings.Builder
			if code := runCommand([]string{"-o", format, path}, &out, io.Discard); code != 1 {
				t.Fatalf("exit = %d, want 1", code)
			}
			switch format {
			case "json":
				var rep runReport
				if err := json.Unmarshal([]byte(out.String()), &rep); err != nil {
					t.Fatal(err)
				}
				if rep.OK || rep.Passed != 2 || rep.Failed != 1 || rep.NotRun != 1 || len(rep.Steps) != 3 {
					t.Fatalf("incorrect history: %+v", rep)
				}
				first := rep.Steps[0]
				if first.Name != "Request original" || first.URL != srv.URL+"/original" || first.Captures["target"] != "changed" || len(first.Asserts) != 1 || !first.Asserts[0].Pass {
					t.Fatalf("lost execution-time values: %+v", first)
				}
				if rep.Steps[2].URL != srv.URL+"/original" {
					t.Fatalf("reset did not restore variables: %+v", rep.Steps[2])
				}
			case "junit":
				var rep junitSuites
				if err := xml.Unmarshal([]byte(out.String()), &rep); err != nil {
					t.Fatal(err)
				}
				if rep.Tests != 3 || rep.Failures != 1 || rep.Suites[0].Cases[0].Name != "GET Request original" {
					t.Fatalf("incorrect history: %+v", rep)
				}
			case "pretty":
				if !strings.Contains(out.String(), "Request original") || !strings.Contains(out.String(), "2 passed, 1 failed, 1 not run") {
					t.Fatalf("incorrect history: %s", out.String())
				}
			}
		})
	}
}

func TestRunCommandReportsExpansionFailures(t *testing.T) {
	for _, request := range []string{"GET {{missing}}/test", "POST http://unused.invalid\n\n< missing-body.json"} {
		t.Run(request, func(t *testing.T) {
			path := writePlan(t, "", request)
			var out strings.Builder
			if code := runCommand([]string{"-o", "json", path}, &out, io.Discard); code != 1 {
				t.Fatalf("exit = %d", code)
			}
			var rep runReport
			if err := json.Unmarshal([]byte(out.String()), &rep); err != nil {
				t.Fatal(err)
			}
			if rep.Failed != 1 || rep.NotRun != 0 || len(rep.Steps) != 1 || rep.Steps[0].Error == "" {
				t.Fatalf("missing failed attempt: %+v", rep)
			}
		})
	}
}

func TestRunCommandFilterStableAcrossCaptures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "changed")
	}))
	defer srv.Close()
	path := writePlan(t, srv.URL, `@label = keep
###
# @name keep first
# @capture label = body
GET {{host}}/first
###
# @name {{label}} second
GET {{host}}/second
`)
	var out strings.Builder
	if code := runCommand([]string{"--filter", "keep", "-o", "json", path}, &out, io.Discard); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var rep runReport
	if err := json.Unmarshal([]byte(out.String()), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Passed != 2 || rep.NotRun != 0 || len(rep.Steps) != 2 {
		t.Fatalf("filter changed during run: %+v", rep)
	}
	if rep.Steps[1].Name != "changed second" {
		t.Fatalf("name was not captured at execution: %+v", rep.Steps[1])
	}
}
