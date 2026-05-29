package httpfile

import (
	"regexp"
	"testing"
	"time"
)

// stubDynamics pins the clock and RNG so dynamic-variable output is deterministic,
// restoring the production backings when the test ends.
func stubDynamics(t *testing.T) {
	t.Helper()
	origNow, origRand := now, randIntn
	now = func() time.Time { return time.Date(2021, 3, 14, 15, 9, 26, 0, time.UTC) }
	randIntn = func(n int) int { return n / 2 }
	t.Cleanup(func() { now, randIntn = origNow, origRand })
}

// TestExpandDynamic covers each recognized dynamic variable through the public
// Expand path, including arg parsing and the unknown-name passthrough.
func TestExpandDynamic(t *testing.T) {
	stubDynamics(t)
	t.Setenv("LAZYHTTP_TEST_ENV", "from-env")

	v := Vars{"host": "example.com"}
	cases := []struct {
		in, want string
	}{
		{"{{$timestamp}}", "1615734566"},
		{"{{$isoTimestamp}}", "2021-03-14T15:09:26Z"},
		{"{{$randomInt 0 10}}", "5"},
		{"{{$randomInt}}", "500"},
		{"{{$randomInt bad args}}", "500"},
		{"{{$datetime rfc1123}}", "Sun, 14 Mar 2021 15:09:26 UTC"},
		{"{{$datetime iso8601}}", "2021-03-14T15:09:26Z"},
		{"{{$processEnv LAZYHTTP_TEST_ENV}}", "from-env"},
		{"host={{host}}", "host=example.com"},
		{"{{$unknown}}", "{{$unknown}}"},   // unrecognized dynamic stays literal
		{"{{missing}}", "{{missing}}"},     // unknown map var stays literal
		{"a {{host}} {{$timestamp}}", "a example.com 1615734566"},
	}
	for _, c := range cases {
		if got := v.Expand(c.in); got != c.want {
			t.Errorf("Expand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestExpandUUID verifies {{$uuid}} and {{$guid}} produce well-formed v4 UUIDs
// and that two occurrences in one string resolve independently.
func TestExpandUUID(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for _, name := range []string{"$uuid", "$guid"} {
		got := Vars{}.Expand("{{" + name + "}}")
		if !re.MatchString(got) {
			t.Errorf("Expand({{%s}}) = %q, not a v4 UUID", name, got)
		}
	}

	out := Vars{}.Expand("{{$uuid}} {{$uuid}}")
	parts := regexp.MustCompile(`\s+`).Split(out, -1)
	if len(parts) != 2 || parts[0] == parts[1] {
		t.Errorf("two {{$uuid}} resolved to %q; expected distinct values", out)
	}
}
