package main

import (
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"
)

// fixedReport is a deterministic multi-step result set the formatter tests render
// against: one passing HTTP step with a capture, one failing HTTP step, and one
// shell step.
func fixedReport() runReport {
	return runReport{
		OK:     false,
		Passed: 2,
		Failed: 1,
		NotRun: 1,
		Steps: []stepReport{
			{
				Name: "Log in", Kind: "http", Method: "POST", URL: "https://api/login",
				OK: true, Status: "200 OK", StatusCode: 200, DurationMs: 120,
				Captures: map[string]string{"token": "abc"},
				Asserts:  []assertReport{{Assertion: "status == 200", Pass: true, Got: "200"}},
			},
			{
				Name: "Create", Kind: "http", Method: "POST", URL: "https://api/items",
				OK: false, Status: "200 OK", StatusCode: 200, DurationMs: 88,
				Asserts: []assertReport{{Assertion: "status == 201", Pass: false, Got: "200"}},
			},
			{
				Name: "cleanup", Kind: "shell", Method: "SHELL",
				OK: true, Status: "exit 0", ExitCode: 0, DurationMs: 3,
			},
		},
	}
}

func TestWriteJSON(t *testing.T) {
	var b strings.Builder
	writeJSON(&b, fixedReport())

	var got runReport
	if err := json.Unmarshal([]byte(b.String()), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b.String())
	}
	if got.OK || got.Passed != 2 || got.Failed != 1 || got.NotRun != 1 {
		t.Errorf("summary round-trip wrong: %+v", got)
	}
	if len(got.Steps) != 3 {
		t.Fatalf("want 3 steps, got %d", len(got.Steps))
	}
	if got.Steps[0].Captures["token"] != "abc" {
		t.Errorf("capture lost in JSON: %+v", got.Steps[0])
	}
	if got.Steps[1].OK {
		t.Error("failing step should serialize ok=false")
	}
}

func TestWriteJUnit(t *testing.T) {
	var b strings.Builder
	writeJUnit(&b, fixedReport(), "plan.http")

	if !strings.HasPrefix(b.String(), xml.Header) {
		t.Error("missing XML header")
	}
	var doc junitSuites
	if err := xml.Unmarshal([]byte(b.String()), &doc); err != nil {
		t.Fatalf("output is not valid XML: %v\n%s", err, b.String())
	}
	if doc.Tests != 3 || doc.Failures != 1 {
		t.Errorf("suites attrs wrong: tests=%d failures=%d", doc.Tests, doc.Failures)
	}
	if len(doc.Suites) != 1 || len(doc.Suites[0].Cases) != 3 {
		t.Fatalf("want 1 suite of 3 cases, got %+v", doc.Suites)
	}
	cases := doc.Suites[0].Cases
	if cases[0].Failure != nil {
		t.Error("passing step should have no <failure>")
	}
	if cases[1].Failure == nil || !strings.Contains(cases[1].Failure.Body, "status == 201") {
		t.Errorf("failing step should carry a <failure> naming the assertion: %+v", cases[1].Failure)
	}
}

func TestHTTPStatus(t *testing.T) {
	cases := map[int]string{
		200: "200 OK",
		404: "404 Not Found",
		299: "299", // non-standard: no reason phrase, and no dangling space
		0:   "0",
	}
	for code, want := range cases {
		if got := httpStatus(code); got != want {
			t.Errorf("httpStatus(%d) = %q, want %q", code, got, want)
		}
	}
}

// TestWriteJUnitSanitizesControlBytes verifies a failure body carrying control
// bytes (as a binary response would) still yields XML a parser accepts: the
// illegal bytes are dropped while tab/newline survive.
func TestWriteJUnitSanitizesControlBytes(t *testing.T) {
	rep := runReport{
		OK: false, Failed: 1, Steps: []stepReport{{
			Name: "binary", Kind: "http", Method: "GET", OK: false,
			Status: "500 Internal Server Error",
			Error:  "boom\x00\x01\x08\x1f\tok\nline", // NUL and friends, plus legal tab/newline
		}},
	}

	var b strings.Builder
	writeJUnit(&b, rep, "plan.http")

	var doc junitSuites
	if err := xml.Unmarshal([]byte(b.String()), &doc); err != nil {
		t.Fatalf("output is not valid XML: %v\n%q", err, b.String())
	}
	body := doc.Suites[0].Cases[0].Failure.Body
	if strings.ContainsAny(body, "\x00\x01\x08\x1f") {
		t.Errorf("control bytes survived sanitization: %q", body)
	}
	if !strings.Contains(body, "\tok\nline") {
		t.Errorf("tab/newline should be preserved: %q", body)
	}
}

func TestWritePretty(t *testing.T) {
	var plain strings.Builder
	writePretty(&plain, fixedReport(), false, false)
	out := plain.String()
	if !strings.Contains(out, "✓ POST Log in → 200 OK") {
		t.Errorf("missing passing step line:\n%s", out)
	}
	if !strings.Contains(out, "✗ assert: status == 201 (got \"200\")") {
		t.Errorf("missing failing assertion detail:\n%s", out)
	}
	if !strings.Contains(out, "2 passed, 1 failed, 1 not run") {
		t.Errorf("missing summary:\n%s", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("color=false must emit no ANSI codes:\n%q", out)
	}

	// quiet drops the per-step lines but keeps the tally.
	var quiet strings.Builder
	writePretty(&quiet, fixedReport(), true, false)
	if strings.Contains(quiet.String(), "Log in") {
		t.Errorf("quiet should suppress per-step lines:\n%s", quiet.String())
	}
	if !strings.Contains(quiet.String(), "2 passed, 1 failed") {
		t.Errorf("quiet should keep the summary:\n%s", quiet.String())
	}

	// color=true wraps marks in ANSI.
	var colored strings.Builder
	writePretty(&colored, fixedReport(), false, true)
	if !strings.Contains(colored.String(), ansiGreen) || !strings.Contains(colored.String(), ansiRed) {
		t.Errorf("color=true should emit ANSI codes:\n%q", colored.String())
	}
}
