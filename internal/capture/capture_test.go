package capture

import (
	"net/http"
	"strings"
	"testing"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

func TestEval(t *testing.T) {
	r := step.Result{
		StatusCode: 201,
		Header:     http.Header{"Location": {"/posts/42"}},
		Body: `{
		  "id": 42,
		  "title": "hi",
		  "tags": ["a", "b"],
		  "author": {"name": "Ada"},
		  "items": [{"id": 7}, {"id": 9}]
		}`,
	}

	cases := []struct {
		expr string
		want string
		ok   bool
	}{
		{"status", "201", true},
		{"header.Location", "/posts/42", true},
		{"json.id", "42", true},
		{"$.title", "hi", true},
		{"author.name", "Ada", true},
		{"tags[1]", "b", true},
		{"items[1].id", "9", true},
		{"json.missing", "", false},
		{"items[9].id", "", false},
	}
	for _, c := range cases {
		got, ok := Eval(c.expr, r)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("Eval(%q) = (%q, %v), want (%q, %v)", c.expr, got, ok, c.want, c.ok)
		}
	}

	// A single Evaluator reused across many expressions (the runner's hot path,
	// where it parses the body once) must yield the same results as the
	// single-shot Eval calls above.
	eval := For(r)
	for _, c := range cases {
		got, ok := eval.Eval(c.expr)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("For(r).Eval(%q) = (%q, %v), want (%q, %v)", c.expr, got, ok, c.want, c.ok)
		}
	}
}

func TestCheck(t *testing.T) {
	r := step.Result{
		StatusCode: 201,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       `{"id": 42, "title": "hi"}`,
	}

	cases := []struct {
		expr, op, want string
		neg            bool
		pass           bool
	}{
		{expr: "status", op: "==", want: "201", pass: true},
		{expr: "status", op: "==", want: "200", pass: false},
		{expr: "status", op: "!=", want: "500", pass: true},
		{expr: "json.id", op: "==", want: "42", pass: true},
		{expr: "json.title", op: "contains", want: "h", pass: true},
		{expr: "header.Content-Type", op: "contains", want: "json", pass: true},
		{expr: "json.id", op: "exists", pass: true},
		{expr: "json.missing", op: "exists", pass: false},
		{expr: "json.id", op: "weirdop", want: "42", pass: false},

		// quote tolerance lives in Check now, not the parser
		{expr: "status", op: "==", want: `"201"`, pass: true},

		// in: membership over a comma-separated set, whitespace tolerated
		{expr: "status", op: "in", want: "200,204", pass: false},
		{expr: "status", op: "in", want: "200, 201, 204", pass: true},
		{expr: "status", op: "in", want: "201", pass: true},

		// numeric comparison (both sides parsed as numbers)
		{expr: "status", op: ">", want: "200", pass: true},
		{expr: "status", op: ">", want: "201", pass: false},
		{expr: "status", op: ">=", want: "201", pass: true},
		{expr: "status", op: "<", want: "300", pass: true},
		{expr: "status", op: "<=", want: "201", pass: true},
		{expr: "json.id", op: ">", want: "40", pass: true},
		{expr: "json.title", op: ">", want: "0", pass: false}, // "hi" isn't numeric

		// matches: RE2 regex, partial unless anchored
		{expr: "json.title", op: "matches", want: "^hi$", pass: true},
		{expr: "json.title", op: "matches", want: "^bye", pass: false},
		{expr: "status", op: "matches", want: `\d{3}`, pass: true},
		{expr: "json.title", op: "matches", want: "(", pass: false}, // invalid regexp

		// negation composes with every operator
		{expr: "status", op: "in", want: "200,204", neg: true, pass: true},
		{expr: "status", op: "==", want: "201", neg: true, pass: false},
		{expr: "json.title", op: "contains", want: "x", neg: true, pass: true},
		{expr: "json.missing", op: "exists", neg: true, pass: true},
		{expr: "json.title", op: "matches", want: "^hi$", neg: true, pass: false},
		// negating an unknown operator still never passes
		{expr: "json.id", op: "weirdop", want: "42", neg: true, pass: false},
	}
	for _, c := range cases {
		out := Check(step.Assertion{Expr: c.expr, Op: c.op, Want: c.want, Negated: c.neg}, r)
		if out.Pass != c.pass {
			t.Errorf("Check(%s %s%s %q) = %v, want %v (got %q, detail %q)",
				c.expr, negPrefix(c.neg), c.op, c.want, out.Pass, c.pass, out.Got, out.Detail)
		}
	}
}

func negPrefix(neg bool) string {
	if neg {
		return "not "
	}
	return ""
}

// TestCheckDetail verifies the human-readable failure reason for the cases where
// the resolved value alone doesn't explain the failure.
func TestCheckDetail(t *testing.T) {
	r := step.Result{StatusCode: 200, Body: `{"title": "hi"}`}
	cases := []struct {
		op, want, substr string
	}{
		{">", "100", ""},                          // numeric pass: no detail
		{">", "abc", "right is not numeric"},      // bad RHS
		{"matches", "(", "invalid regexp"},        // bad pattern
		{"bogus", "x", "unknown operator: bogus"}, // unrecognized op
	}
	for _, c := range cases {
		out := Check(step.Assertion{Expr: "status", Op: c.op, Want: c.want}, r)
		if c.substr == "" {
			if out.Detail != "" {
				t.Errorf("Check(status %s %q) detail = %q, want empty", c.op, c.want, out.Detail)
			}
			continue
		}
		if !strings.Contains(out.Detail, c.substr) {
			t.Errorf("Check(status %s %q) detail = %q, want substring %q", c.op, c.want, out.Detail, c.substr)
		}
	}
}
