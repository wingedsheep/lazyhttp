// Package step defines the data model shared across lazyhttp: the parsed
// steps of a plan and the results of executing them.
package step

import (
	"net/http"
	"time"
)

// Kind distinguishes an HTTP request from a shell command.
type Kind int

const (
	KindHTTP Kind = iota
	KindShell
)

// Step is a single, ordered entry in a test plan. Method/URL/Headers/Body hold
// raw templates with {{var}} placeholders intact; they're expanded at execution
// time so captures from earlier steps can flow in.
type Step struct {
	Name     string
	Group    string // optional section heading; empty means ungrouped
	Kind     Kind
	Method   string            // HTTP only
	URL      string            // HTTP only
	Headers  map[string]string // HTTP only
	Body     string            // HTTP request body, or the shell script

	// BodyFile names a file whose contents are sent as the request body, from a
	// `< path` / `<@ path` line; empty when the body is inline. BodyFileVars is
	// true for the `<@` form, which expands {{vars}} in the file contents. The
	// path is resolved relative to the plan file's directory at execution time.
	BodyFile     string
	BodyFileVars bool

	Captures []Capture         // values to extract from the response
	Asserts  []Assertion       // checks to run against the response
	Reset    bool              // when this step succeeds, reset the rest of the plan
	Raw      string            // original text of the block, for the detail view
}

// Capture extracts a value from a step's response into a named variable that
// later steps can reference as {{Name}}.
type Capture struct {
	Name string // variable to set, e.g. "postId"
	Expr string // source expression, e.g. "json.id", "header.Location", "status"
}

// Assertion checks a value from the response. Expr is evaluated the same way as
// a Capture; Op compares it against Want. Op "exists" ignores Want.
type Assertion struct {
	Expr string // left-hand expression, e.g. "status", "json.id"
	Op   string // ==, !=, contains, exists
	Want string // expected value (empty for "exists")
	Raw  string // original directive text, for display
}

// AssertOutcome is the result of evaluating one Assertion against a response.
type AssertOutcome struct {
	Assertion Assertion
	Pass      bool
	Got       string // the value the expression resolved to
}

// Status tracks where a step is in its lifecycle.
type Status int

const (
	Pending Status = iota
	Running
	Done
	Failed
)

// Result is the outcome of executing a Step. The zero value (Status == Pending)
// means the step has not been run yet.
type Result struct {
	Status     Status
	StatusCode int             // HTTP response code
	ExitCode   int             // shell exit code
	Header     http.Header     // HTTP response headers
	Body       string          // response body, or combined stdout+stderr
	Duration   time.Duration   // wall-clock time of the execution
	Err        error           // transport/spawn error, if any
	Asserts    []AssertOutcome // evaluated assertions, if any
}

// OK reports whether a finished result represents success: a 2xx response for
// HTTP (or zero exit code for shell) with every assertion passing.
func (r Result) OK() bool {
	if r.Err != nil {
		return false
	}
	if !r.AssertsPass() {
		return false
	}
	if r.StatusCode != 0 {
		return r.StatusCode >= 200 && r.StatusCode < 300
	}
	return r.ExitCode == 0
}

// AssertsPass reports whether every assertion on the result passed (vacuously
// true when there are none).
func (r Result) AssertsPass() bool {
	for _, a := range r.Asserts {
		if !a.Pass {
			return false
		}
	}
	return true
}
