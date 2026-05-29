package runner

import (
	"strings"
	"testing"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

func TestUnresolved(t *testing.T) {
	s := step.Step{
		Kind:    step.KindHTTP,
		URL:     "{{api}}/auth/login",
		Headers: map[string]string{"Authorization": "Bearer {{$auth.token(\"id\")}}", "X-Trace": "{{traceId}}"},
		Body:    `{"token":"{{login.response.body.$.token}}","api":"{{api}}"}`,
	}
	got := Unresolved(s)

	// {{api}} (twice, deduped) and {{traceId}} are genuinely missing; the auth
	// token and the response reference are filled by later stages and excluded.
	want := map[string]bool{"api": true, "traceId": true}
	if len(got) != len(want) {
		t.Fatalf("Unresolved = %v, want keys %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected unresolved token %q (got %v)", g, got)
		}
	}
}

func TestUnresolvedNoneWhenResolved(t *testing.T) {
	s := step.Step{Kind: step.KindHTTP, URL: "https://example.com/users"}
	if got := Unresolved(s); len(got) != 0 {
		t.Errorf("Unresolved = %v, want none", got)
	}
}

func TestUnresolvedError(t *testing.T) {
	one := UnresolvedError([]string{"api"}, "press E").Error()
	if !strings.Contains(one, "variable {{api}}") || !strings.Contains(one, "press E") {
		t.Errorf("single-var message = %q", one)
	}
	two := UnresolvedError([]string{"api", "host"}, "").Error()
	if !strings.Contains(two, "variables {{api}}, {{host}}") {
		t.Errorf("multi-var message = %q", two)
	}
}
