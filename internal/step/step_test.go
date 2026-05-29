package step

import "testing"

func TestResultOK(t *testing.T) {
	cases := []struct {
		name string
		r    Result
		want bool
	}{
		{"2xx", Result{StatusCode: 200}, true},
		{"3xx without no-redirect", Result{StatusCode: 302}, false},
		{"3xx with no-redirect", Result{StatusCode: 302, NoRedirect: true}, true},
		{"4xx with no-redirect still fails", Result{StatusCode: 404, NoRedirect: true}, false},
		{"5xx", Result{StatusCode: 500}, false},
		{"transport error", Result{StatusCode: 200, Err: errString("boom")}, false},
		{"failed assertion", Result{StatusCode: 200, Asserts: []AssertOutcome{{Pass: false}}}, false},
		{"passing assertion", Result{StatusCode: 200, Asserts: []AssertOutcome{{Pass: true}}}, true},
		{"shell zero exit", Result{ExitCode: 0}, true},
		{"shell non-zero exit", Result{ExitCode: 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.OK(); got != tc.want {
				t.Errorf("OK() = %v, want %v", got, tc.want)
			}
		})
	}
}

type errString string

func (e errString) Error() string { return string(e) }
