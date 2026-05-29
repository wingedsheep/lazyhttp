package exec

import (
	"encoding/base64"
	"strings"
)

// encodeBasicAuth applies the IntelliJ / VS Code REST Client convenience for
// Basic auth: an Authorization value that carries raw credentials is base64
// -encoded into a standard `Basic <base64(user:password)>` header. Variable
// expansion has already happened by the time this runs, so `{{vars}}` in the
// credentials are resolved before encoding.
//
// Three credential forms are recognized after a case-insensitive `Basic`
// scheme (matching VS Code, the broader of the two reference implementations):
//
//	Basic user pass    two whitespace-separated tokens -> encode "user:pass"
//	Basic user:pass    one token containing a colon    -> encode it as-is
//	Basic dXNlcjpwYXNz one token, no colon             -> already base64, pass through
//
// Anything that doesn't look like Basic, or carries 0 / 3+ tokens (a password
// with whitespace can't be expressed with the shorthand — pre-encode those),
// is returned unchanged.
func encodeBasicAuth(value string) string {
	fields := strings.Fields(value)
	if len(fields) < 2 || !strings.EqualFold(fields[0], "Basic") {
		return value
	}
	creds := fields[1:]

	switch len(creds) {
	case 1:
		// A single token with a colon is the raw user:password form; without
		// one it's assumed already base64-encoded and passes through untouched.
		if !strings.Contains(creds[0], ":") {
			return value
		}
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(creds[0]))
	case 2:
		joined := creds[0] + ":" + creds[1]
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(joined))
	default:
		return value
	}
}
