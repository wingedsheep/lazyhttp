// Package capture extracts values from a step's response so they can be reused
// as variables by later steps.
package capture

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

// Eval resolves a capture expression against a result. It is a single-shot
// convenience over For(r).Eval; evaluating several expressions against the same
// result is cheaper through a shared Evaluator (see For), which parses the JSON
// body only once.
func Eval(expr string, r step.Result) (value string, ok bool) {
	return For(r).Eval(expr)
}

// Check evaluates one assertion against a result, the single-shot counterpart to
// For(r).Check.
func Check(a step.Assertion, r step.Result) step.AssertOutcome {
	return For(r).Check(a)
}

// Evaluator resolves capture and assertion expressions against one result,
// parsing its JSON body at most once no matter how many JSON-path expressions
// are evaluated. A step's captures and assertions — or several inline
// {{name.response...}} references to the same response — share one Evaluator so
// the body is unmarshalled a single time instead of per expression.
type Evaluator struct {
	r       step.Result
	doc     any  // the parsed JSON body, valid once decoded && docOK
	docOK   bool // whether the body parsed as JSON
	decoded bool // whether decoding has been attempted yet
}

// For returns an Evaluator bound to result r.
func For(r step.Result) *Evaluator { return &Evaluator{r: r} }

// Eval resolves a capture expression against the bound result and returns the
// value as a string. Supported expressions:
//
//	status            the HTTP status code (or shell exit code)
//	header.Name       a response header value
//	body              the entire response body
//	json.a.b[0].c     a path into the JSON body ("$." and a bare path also work)
//
// ok is false when the path can't be resolved (e.g. missing JSON key).
func (e *Evaluator) Eval(expr string) (value string, ok bool) {
	expr = strings.TrimSpace(expr)
	switch {
	case expr == "status":
		if e.r.StatusCode != 0 {
			return strconv.Itoa(e.r.StatusCode), true
		}
		return strconv.Itoa(e.r.ExitCode), true
	case expr == "body":
		return e.r.Body, true
	case strings.HasPrefix(expr, "header."):
		name := strings.TrimPrefix(expr, "header.")
		if e.r.Header == nil {
			return "", false
		}
		v := e.r.Header.Get(name)
		return v, v != ""
	default:
		doc, ok := e.json()
		if !ok {
			return "", false
		}
		return walkJSON(doc, expr)
	}
}

// Check evaluates an assertion against the bound result. The left-hand
// expression is resolved like a capture, then compared per the operator. A "not"
// prefix (a.Negated) inverts the operator's verdict — except for an unknown
// operator, which never passes regardless of negation.
func (e *Evaluator) Check(a step.Assertion) step.AssertOutcome {
	got, found := e.Eval(a.Expr)
	out := step.AssertOutcome{Assertion: a, Got: got}
	pass, detail, known := evalOp(a.Op, a.Want, got, found)
	if !known {
		out.Detail = detail // Pass stays false; negating an unknown op is still false
		return out
	}
	out.Pass = pass != a.Negated
	out.Detail = detail
	return out
}

// json returns the bound result's body parsed as a JSON document, unmarshalling
// it on the first call and reusing the result thereafter. ok is false when the
// body isn't valid JSON.
func (e *Evaluator) json() (doc any, ok bool) {
	if !e.decoded {
		e.decoded = true
		var parsed any
		if json.Unmarshal([]byte(e.r.Body), &parsed) == nil {
			e.doc, e.docOK = parsed, true
		}
	}
	return e.doc, e.docOK
}

// evalOp applies one assertion operator, returning its verdict before any
// negation, an optional failure detail (for cases where the resolved value
// alone doesn't explain the failure), and whether the operator is recognized.
func evalOp(op, want, got string, found bool) (pass bool, detail string, known bool) {
	switch op {
	case "exists":
		return found, "", true
	case "==":
		return found && equalValues(got, unquote(want)), "", true
	case "!=":
		return found && !equalValues(got, unquote(want)), "", true
	case "contains":
		return found && strings.Contains(got, unquote(want)), "", true
	case "in":
		if !found {
			return false, "", true
		}
		for _, w := range strings.Split(want, ",") {
			if got == unquote(strings.TrimSpace(w)) {
				return true, "", true
			}
		}
		return false, "", true
	case ">", ">=", "<", "<=":
		if !found {
			return false, "", true
		}
		l, err := strconv.ParseFloat(strings.TrimSpace(got), 64)
		if err != nil {
			return false, fmt.Sprintf("left is not numeric: %q", got), true
		}
		rhs, err := strconv.ParseFloat(unquote(strings.TrimSpace(want)), 64)
		if err != nil {
			return false, fmt.Sprintf("right is not numeric: %q", want), true
		}
		return numericCompare(op, l, rhs), "", true
	case "matches":
		if !found {
			return false, "", true
		}
		re, err := regexp.Compile(unquote(want)) // RE2 syntax; anchors are the author's job
		if err != nil {
			return false, fmt.Sprintf("invalid regexp: %v", err), true
		}
		return re.MatchString(got), "", true
	default:
		return false, "unknown operator: " + op, false
	}
}

func numericCompare(op string, l, r float64) bool {
	switch op {
	case ">":
		return l > r
	case ">=":
		return l >= r
	case "<":
		return l < r
	case "<=":
		return l <= r
	}
	return false
}

// equalValues compares a resolved value against an (already unquoted) operand
// for == / !=. When both sides parse as numbers it compares them numerically, so
// `json.price == 9.90` matches a body that stringifies the value to "9.9";
// otherwise it falls back to a plain string compare (which still covers strings
// and booleans like "true"). This mirrors the numeric handling of >/>=/</<=, so
// equality and ordering agree on what counts as a number.
func equalValues(got, want string) bool {
	if g, err := strconv.ParseFloat(strings.TrimSpace(got), 64); err == nil {
		if w, err := strconv.ParseFloat(strings.TrimSpace(want), 64); err == nil {
			return g == w
		}
	}
	return got == want
}

// unquote strips one matched pair of surrounding single or double quotes, so
// `== "201"` and `== 201` are equivalent. It removes only a genuine matched pair
// — not a lone or mismatched quote — so an operand whose quote is meaningful
// (notably a `matches` regex like `"\d+"` vs. an intended `\d+`) keeps any quote
// that isn't actually wrapping the value.
func unquote(s string) string {
	if len(s) >= 2 {
		if q := s[0]; (q == '"' || q == '\'') && s[len(s)-1] == q {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// walkJSON walks a dotted/bracketed path into an already-decoded JSON document.
// Decoding is the Evaluator's job (so a body is parsed once across many paths);
// this stays a pure walk over the result.
func walkJSON(doc any, path string) (string, bool) {
	path = strings.TrimPrefix(path, "json.")
	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, "$")
	path = strings.TrimPrefix(path, ".")

	cur := doc
	for _, tok := range tokenize(path) {
		switch {
		case tok.index >= 0:
			arr, ok := cur.([]any)
			if !ok || tok.index >= len(arr) {
				return "", false
			}
			cur = arr[tok.index]
		default:
			obj, ok := cur.(map[string]any)
			if !ok {
				return "", false
			}
			cur, ok = obj[tok.key]
			if !ok {
				return "", false
			}
		}
	}
	return stringify(cur)
}

// token is either a map key or an array index (index >= 0).
type token struct {
	key   string
	index int
}

// tokenize splits "a.b[0].c" into [a, b, [0], c] style tokens.
func tokenize(path string) []token {
	var (
		toks []token
		cur  strings.Builder
	)
	emitKey := func() {
		if cur.Len() > 0 {
			toks = append(toks, token{key: cur.String(), index: -1})
			cur.Reset()
		}
	}
	for _, ch := range path {
		switch ch {
		case '.':
			emitKey()
		case '[':
			emitKey()
		case ']':
			if n, err := strconv.Atoi(cur.String()); err == nil {
				toks = append(toks, token{index: n})
			}
			cur.Reset()
		default:
			cur.WriteRune(ch)
		}
	}
	emitKey()
	return toks
}

// stringify renders a resolved JSON value as a plain string (integers without a
// trailing .0, everything else via its natural representation).
func stringify(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case bool:
		return strconv.FormatBool(t), true
	case float64:
		if t == math.Trunc(t) && !math.IsInf(t, 0) {
			return strconv.FormatInt(int64(t), 10), true
		}
		return strconv.FormatFloat(t, 'g', -1, 64), true
	case nil:
		return "", false
	default:
		b, err := json.Marshal(t)
		return string(b), err == nil
	}
}
