package main

import (
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

	plan.Run(context.Background(), include)
	rep := buildReport(plan, include, eligible)

	switch output {
	case "json":
		writeJSON(out, rep)
	case "junit":
		writeJUnit(out, rep, fs.Arg(0))
	default:
		writePretty(out, rep, *quiet, useColor(out))
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
	return func(i int) bool {
		s := plan.Steps[i]
		hay := strings.ToLower(s.Method + " " + plan.Label(i) + " " + s.Group)
		return strings.Contains(hay, q)
	}
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

// buildReport collects the outcome of every step that ran into a runReport.
// Steps the filter excluded, and steps left Pending after the chain stopped on a
// failure, are not counted; NotRun records how many of the eligible steps never
// executed because of an earlier failure.
func buildReport(plan *runner.Plan, include func(i int) bool, eligible int) runReport {
	var rep runReport
	ran := 0
	for i := range plan.Steps {
		if include != nil && !include(i) {
			continue
		}
		r := plan.Results[i]
		if r.Status == step.Pending {
			continue
		}
		ran++
		sr := buildStepReport(plan, i, r)
		if sr.OK {
			rep.Passed++
		} else {
			rep.Failed++
		}
		rep.Steps = append(rep.Steps, sr)
	}
	rep.NotRun = eligible - ran
	rep.OK = rep.Failed == 0
	return rep
}

func buildStepReport(plan *runner.Plan, i int, r step.Result) stepReport {
	s := plan.Steps[i]
	sr := stepReport{
		Name:       plan.Label(i),
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
		// Best-effort expansion for display; after a @reset, captured {{vars}} in
		// the URL may no longer resolve and are left as-is.
		sr.URL = plan.Vars.Expand(s.URL)
		sr.StatusCode = r.StatusCode
		sr.Status = fmt.Sprintf("%d %s", r.StatusCode, http.StatusText(r.StatusCode))
	}
	if r.Err != nil {
		sr.Error = r.Err.Error()
		sr.Status = "error: " + r.Err.Error()
	}

	for _, c := range s.Captures {
		if val, ok := capture.Eval(c.Expr, r); ok {
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
