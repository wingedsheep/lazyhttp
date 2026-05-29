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

// Eval resolves a capture expression against a result and returns the value as
// a string. Supported expressions:
//
//	status            the HTTP status code (or shell exit code)
//	header.Name       a response header value
//	body              the entire response body
//	json.a.b[0].c     a path into the JSON body ("$." and a bare path also work)
//
// ok is false when the path can't be resolved (e.g. missing JSON key).
func Eval(expr string, r step.Result) (value string, ok bool) {
	expr = strings.TrimSpace(expr)
	switch {
	case expr == "status":
		if r.StatusCode != 0 {
			return strconv.Itoa(r.StatusCode), true
		}
		return strconv.Itoa(r.ExitCode), true
	case expr == "body":
		return r.Body, true
	case strings.HasPrefix(expr, "header."):
		name := strings.TrimPrefix(expr, "header.")
		if r.Header == nil {
			return "", false
		}
		v := r.Header.Get(name)
		return v, v != ""
	default:
		return jsonPath(r.Body, expr)
	}
}

// Check evaluates an assertion against a result. The left-hand expression is
// resolved like a capture, then compared per the operator. A "not" prefix
// (a.Negated) inverts the operator's verdict — except for an unknown operator,
// which never passes regardless of negation.
func Check(a step.Assertion, r step.Result) step.AssertOutcome {
	got, found := Eval(a.Expr, r)
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

// evalOp applies one assertion operator, returning its verdict before any
// negation, an optional failure detail (for cases where the resolved value
// alone doesn't explain the failure), and whether the operator is recognized.
func evalOp(op, want, got string, found bool) (pass bool, detail string, known bool) {
	switch op {
	case "exists":
		return found, "", true
	case "==":
		return found && got == unquote(want), "", true
	case "!=":
		return found && got != unquote(want), "", true
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
		re, err := regexp.Compile(want) // RE2 syntax; anchors are the author's job
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

// unquote strips a single layer of surrounding single or double quotes, so
// `== "201"` and `== 201` are equivalent. Operators that compare structurally
// (matches) skip it on purpose.
func unquote(s string) string {
	return strings.Trim(s, `"'`)
}

// jsonPath walks a dotted/bracketed path into a JSON document.
func jsonPath(body, path string) (string, bool) {
	path = strings.TrimPrefix(path, "json.")
	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, "$")
	path = strings.TrimPrefix(path, ".")

	var doc any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		return "", false
	}

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
