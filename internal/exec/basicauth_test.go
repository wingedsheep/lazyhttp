package exec

import "testing"

// TestEncodeBasicAuth covers the three credential forms lazyhttp encodes and the
// cases it must leave untouched (already-encoded, non-Basic, ambiguous).
func TestEncodeBasicAuth(t *testing.T) {
	// base64("alice:s3cret") == "YWxpY2U6czNjcmV0"
	const encoded = "Basic YWxpY2U6czNjcmV0"

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"two tokens", "Basic alice s3cret", encoded},
		{"colon form", "Basic alice:s3cret", encoded},
		{"already base64 passes through", encoded, encoded},
		{"scheme is case-insensitive", "basic alice s3cret", encoded},
		{"extra whitespace between tokens", "Basic   alice    s3cret", encoded},
		{"non-basic scheme untouched", "Bearer sometoken", "Bearer sometoken"},
		{"empty value untouched", "", ""},
		{"three tokens are ambiguous, untouched", "Basic alice s3 cret", "Basic alice s3 cret"},
		{"bare scheme untouched", "Basic", "Basic"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := encodeBasicAuth(tc.in); got != tc.want {
				t.Errorf("encodeBasicAuth(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
