package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// ANSI colours for pretty output; emitted only when useColor is true.
const (
	ansiReset = "\x1b[0m"
	ansiGreen = "\x1b[32m"
	ansiRed   = "\x1b[31m"
	ansiDim   = "\x1b[2m"
)

// writePretty renders the human summary: a ✓/✗ line per step (status + duration)
// and a line per assertion, then a final tally. When quiet, only the tally is
// printed. When color, the marks and duration are ANSI-coloured.
func writePretty(out io.Writer, rep runReport, quiet, color bool) {
	paint := func(s, code string) string {
		if !color {
			return s
		}
		return code + s + ansiReset
	}

	if !quiet {
		for _, s := range rep.Steps {
			mark, code := "✓", ansiGreen
			if !s.OK {
				mark, code = "✗", ansiRed
			}
			fmt.Fprintf(out, "%s %s %s → %s · %s\n",
				paint(mark, code), s.Method, s.Name, s.Status, paint(ms(s.DurationMs), ansiDim))

			for _, a := range s.Asserts {
				amark, acode := "✓", ansiGreen
				if !a.Pass {
					amark, acode = "✗", ansiRed
				}
				line := fmt.Sprintf("    %s assert: %s", paint(amark, acode), a.Assertion)
				if !a.Pass {
					line += " (" + assertReason(a) + ")"
				}
				fmt.Fprintln(out, line)
			}
		}
		fmt.Fprintln(out)
	}

	summary := fmt.Sprintf("%d passed, %d failed", rep.Passed, rep.Failed)
	if rep.NotRun > 0 {
		summary += fmt.Sprintf(", %d not run", rep.NotRun)
	}
	fmt.Fprintln(out, summary)
}

// writeJSON emits the report as indented JSON for scripting and dashboards.
func writeJSON(out io.Writer, rep runReport) {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rep)
}

// JUnit XML model — one <testcase> per executed step, so a run drops straight
// into GitHub Actions / GitLab test reporting.
type junitSuites struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Suites   []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

// writeJUnit emits JUnit XML. planPath names the suite. A failed step carries a
// <failure> whose message summarises why (transport error, bad status, or the
// failing assertions).
func writeJUnit(out io.Writer, rep runReport, planPath string) {
	suite := junitSuite{
		Name:     filepath.Base(planPath),
		Tests:    len(rep.Steps),
		Failures: rep.Failed,
	}
	for _, s := range rep.Steps {
		c := junitCase{
			Name:      xmlSafe(s.Method + " " + s.Name),
			Classname: filepath.Base(planPath),
			Time:      fmt.Sprintf("%.3f", float64(s.DurationMs)/1000),
		}
		if !s.OK {
			msg, body := failureSummary(s)
			// A failed step's body can echo the response — which may be binary —
			// so scrub characters illegal in XML before they reach the document.
			c.Failure = &junitFailure{Message: xmlSafe(msg), Body: xmlSafe(body)}
		}
		suite.Cases = append(suite.Cases, c)
	}
	doc := junitSuites{
		Tests:    len(rep.Steps),
		Failures: rep.Failed,
		Suites:   []junitSuite{suite},
	}

	fmt.Fprint(out, xml.Header)
	enc := xml.NewEncoder(out)
	enc.Indent("", "  ")
	_ = enc.Encode(doc)
	fmt.Fprintln(out)
}

// failureSummary returns a short message and a detailed body for a failed step's
// JUnit <failure> element.
func failureSummary(s stepReport) (msg, body string) {
	if s.Error != "" {
		return "request error", s.Error
	}
	var failed []string
	for _, a := range s.Asserts {
		if !a.Pass {
			failed = append(failed, a.Assertion+" ("+assertReason(a)+")")
		}
	}
	if len(failed) > 0 {
		return fmt.Sprintf("%d assertion(s) failed", len(failed)), strings.Join(failed, "\n")
	}
	return "unexpected status " + s.Status, s.Status
}

// assertReason explains a failed assertion: its detail if present, else the value
// the expression resolved to.
func assertReason(a assertReport) string {
	if a.Detail != "" {
		return a.Detail
	}
	return fmt.Sprintf("got %q", a.Got)
}

func ms(d int64) string { return fmt.Sprintf("%dms", d) }

// xmlSafe drops characters that are illegal in XML 1.0, so a step whose response
// body contains binary or control bytes still produces a JUnit file CI parsers
// accept. Tab, newline, and carriage return are kept; the other C0 controls,
// lone UTF-16 surrogate halves, and the non-characters U+FFFE/U+FFFF are removed.
func xmlSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			return r
		case r < 0x20, r >= 0xD800 && r <= 0xDFFF, r == 0xFFFE, r == 0xFFFF:
			return -1 // drop
		default:
			return r
		}
	}, s)
}
