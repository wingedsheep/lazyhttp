package capture

import (
	"net/http"
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
}

func TestCheck(t *testing.T) {
	r := step.Result{
		StatusCode: 201,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       `{"id": 42, "title": "hi"}`,
	}

	cases := []struct {
		expr, op, want string
		pass           bool
	}{
		{"status", "==", "201", true},
		{"status", "==", "200", false},
		{"status", "!=", "500", true},
		{"json.id", "==", "42", true},
		{"json.title", "contains", "h", true},
		{"header.Content-Type", "contains", "json", true},
		{"json.id", "exists", "", true},
		{"json.missing", "exists", "", false},
		{"json.id", "weirdop", "42", false},
	}
	for _, c := range cases {
		out := Check(step.Assertion{Expr: c.expr, Op: c.op, Want: c.want}, r)
		if out.Pass != c.pass {
			t.Errorf("Check(%s %s %q) = %v, want %v (got %q)", c.expr, c.op, c.want, out.Pass, c.pass, out.Got)
		}
	}
}
