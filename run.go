package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-isatty"

	"github.com/wingedsheep/lazyhttp/internal/capture"
	"github.com/wingedsheep/lazyhttp/internal/runner"
	"github.com/wingedsheep/lazyhttp/internal/step"
)

// runReport is the machine-readable outcome of a headless run, shared by every
// output format so pretty/json/junit render the same data.
type runReport struct {
	OK     bool         `json:"ok"`
	Passed int          `json:"passed"`
	Failed int          `json:"failed"`
	NotRun int          `json:"notRun"`
	Steps  []stepReport `json:"steps"`
}

// stepReport is one executed step's outcome. StatusCode/ExitCode are reported by
// Kind; Captures and assertions are included for scripting and dashboards.
type stepReport struct {
	Name       string            `json:"name"`
	Kind       string            `json:"kind"` // "http" | "shell"
	Method     string            `json:"method"`
	URL        string            `json:"url,omitempty"`
	OK         bool              `json:"ok"`
	Status     string            `json:"status"`               // "200 OK", "exit 0", or "error: …"
	StatusCode int               `json:"statusCode,omitempty"` // HTTP only
	ExitCode   int               `json:"exitCode,omitempty"`   // shell only
	DurationMs int64             `json:"durationMs"`
	Error      string            `json:"error,omitempty"`
	Captures   map[string]string `json:"captures,omitempty"`
	Asserts    []assertReport    `json:"assertions,omitempty"`
}

type assertReport struct {
	Assertion string `json:"assertion"`
	Pass      bool   `json:"pass"`
	Got       string `json:"got,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// runCommand is the headless `lazyhttp run <plan.http>` entry point: it executes
// a plan top-to-bottom without the TUI, writes a report to out in the chosen
// format, and returns a process exit code suitable for CI:
//
//	0 — every step that ran was OK and all assertions passed
//	1 — a step failed (transport error, non-2xx status, or a failed assertion)
//	2 — usage / parse / unreadable-plan errors
//
// A failed @assert against an otherwise-successful (2xx) request yields 1, not 0
// — that is the whole point of the runner in a pipeline. The report goes to out
// and diagnostics to errOut, so `--output json|junit > report.xml` stays clean.
func runCommand(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(errOut)
	env := fs.String("env", "", "environment name from http-client.env.json")
	filter := fs.String("filter", "", "run only steps whose method, name or group contains this substring")
	quiet := fs.Bool("quiet", false, "suppress per-step lines; print only the final summary (pretty only)")
	// -o is an alias for --output: both write the same variable, so the last one
	// given on the command line wins and either spelling works.
	var output string
	fs.StringVar(&output, "output", "pretty", "output format: pretty, json, junit")
	fs.StringVar(&output, "o", "pretty", "shorthand for --output")
	fs.Usage = func() {
		fmt.Fprintf(errOut, "Usage: lazyhttp run [--env NAME] [--filter SUBSTR] [--output FMT] [--quiet] <plan.http>\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	switch output {
	case "pretty", "json", "junit":
	default:
		fmt.Fprintf(errOut, "unknown output format %q; valid: pretty, json, junit\n", output)
		return 2
	}

	plan, err := runner.Load(fs.Arg(0), *env)
	if err != nil {
		fmt.Fprintln(errOut, "error:", err)
		return 2
	}

	include := matcher(plan, *filter)
	eligible := countEligible(plan, include)
	if eligible == 0 {
		// Still emit a valid (empty) report so JSON/JUnit consumers don't choke,
		// but flag the likely mistake on stderr.
		fmt.Fprintf(errOut, "no steps match filter %q\n", *filter)
	}

	// Stream a line per step to stderr as the run proceeds, so a long plan shows
	// progress instead of going silent until the final report. It goes to stderr
	// (never the stdout report) and is suppressed by --quiet.
	if !*quiet {
		ran := 0
		plan.OnStepStart = func(i int) {
			ran++
			fmt.Fprintf(errOut, "[%d/%d] %s %s\n", ran, eligible, stepMethod(plan, i), plan.Label(i))
		}
	}

	// Capture history before @reset or later captures mutate live plan state.
	rep := runReport{OK: true, NotRun: eligible}
	plan.OnStepDone = func(_ int, s step.Step, r step.Result) {
		sr := buildStepReport(s, r)
		rep.Steps = append(rep.Steps, sr)
		rep.NotRun--
		if sr.OK {
			rep.Passed++
		} else {
			rep.Failed++
			rep.OK = false
		}
	}
	plan.Run(context.Background(), include)

	// Buffering retains write errors from every renderer, including fmt calls.
	reportOut := bufio.NewWriter(out)
	switch output {
	case "json":
		writeJSON(reportOut, rep)
	case "junit":
		writeJUnit(reportOut, rep, fs.Arg(0))
	default:
		writePretty(reportOut, rep, *quiet, useColor(out))
	}

	if err := reportOut.Flush(); err != nil {
		fmt.Fprintln(errOut, "write report:", err)
		return 1
	}

	if rep.Failed > 0 {
		return 1
	}
	return 0
}

// matcher returns a predicate selecting the steps a non-empty filter matches —
// substring (case-insensitive) against the same haystack the TUI list filters on
// (method, display name, group). An empty filter returns nil, meaning "run all".
func matcher(plan *runner.Plan, filter string) func(i int) bool {
	q := strings.ToLower(strings.TrimSpace(filter))
	if q == "" {
		return nil
	}
	// Freeze selection before execution so captures cannot change which steps
	// match after the progress total has already been calculated.
	selected := make([]bool, len(plan.Steps))
	for i, s := range plan.Steps {
		hay := strings.ToLower(s.Method + " " + plan.Label(i) + " " + s.Group)
		selected[i] = strings.Contains(hay, q)
	}
	return func(i int) bool { return selected[i] }
}

// countEligible returns how many steps were eligible to run: every step when
// include is nil, otherwise the filtered subset.
func countEligible(plan *runner.Plan, include func(i int) bool) int {
	if include == nil {
		return len(plan.Steps)
	}
	n := 0
	for i := range plan.Steps {
		if include(i) {
			n++
		}
	}
	return n
}

// buildStepReport snapshots a completed step using its execution-time request.
func buildStepReport(s step.Step, r step.Result) stepReport {
	sr := stepReport{
		Name:       s.Name,
		OK:         r.OK(),
		DurationMs: r.Duration.Round(time.Millisecond).Milliseconds(),
	}
	if sr.Name == "" {
		sr.Name = s.URL
	}

	switch {
	case s.Kind == step.KindShell:
		sr.Kind, sr.Method = "shell", "SHELL"
		sr.ExitCode = r.ExitCode
		sr.Status = fmt.Sprintf("exit %d", r.ExitCode)
	default:
		sr.Kind, sr.Method = "http", s.Method
		sr.URL = s.URL
		sr.StatusCode = r.StatusCode
		sr.Status = httpStatus(r.StatusCode)
	}
	if r.Err != nil {
		sr.Error = r.Err.Error()
		sr.Status = "error: " + r.Err.Error()
	}

	eval := capture.For(r)
	for _, c := range s.Captures {
		if val, ok := eval.Eval(c.Expr); ok {
			if sr.Captures == nil {
				sr.Captures = make(map[string]string)
			}
			sr.Captures[c.Name] = val
		}
	}
	for _, a := range r.Asserts {
		sr.Asserts = append(sr.Asserts, assertReport{
			Assertion: a.Assertion.Raw,
			Pass:      a.Pass,
			Got:       a.Got,
			Detail:    a.Detail,
		})
	}
	return sr
}

// stepMethod is the verb shown for a step in progress lines and reports: its
// HTTP method, or "SHELL" for a shell step, which has no method of its own.
func stepMethod(plan *runner.Plan, i int) string {
	if plan.Steps[i].Kind == step.KindShell {
		return "SHELL"
	}
	return plan.Steps[i].Method
}

// httpStatus renders an HTTP status line. For a standard code it reads
// "200 OK"; for a non-standard one (http.StatusText returns "" for those) it
// drops the empty reason phrase, so the report shows "299" rather than a
// dangling "299 ".
func httpStatus(code int) string {
	if text := http.StatusText(code); text != "" {
		return fmt.Sprintf("%d %s", code, text)
	}
	return fmt.Sprintf("%d", code)
}

// useColor reports whether to colourize pretty output: only when out is a real
// terminal and NO_COLOR is unset (the de-facto opt-out). Piped/redirected output
// and io.Discard (tests) stay plain.
func useColor(out io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := out.(*os.File)
	return ok && isatty.IsTerminal(f.Fd())
}
