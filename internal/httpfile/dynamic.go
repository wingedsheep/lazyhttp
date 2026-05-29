package httpfile

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// now and randIntn back the dynamic-variable resolvers. They are package vars so
// tests can stub the clock and RNG for deterministic output.
var (
	now      = time.Now
	randIntn = cryptoIntn
)

// cryptoIntn returns a uniformly random int in [0, n) using crypto/rand. For
// n <= 0 it returns 0. It is the production backing for randIntn.
func cryptoIntn(n int) int {
	if n <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	return int(binary.BigEndian.Uint64(b[:]) % uint64(n))
}

// dynamic resolves an IntelliJ-style dynamic variable (the part after the
// leading `$`, e.g. "uuid" or "randomInt") with its space-separated args. The
// second return is false when the name is unrecognized, in which case Expand
// leaves the placeholder untouched.
func dynamic(name string, args []string) (string, bool) {
	switch name {
	case "$uuid", "$guid":
		return uuidV4(), true
	case "$timestamp":
		return strconv.FormatInt(now().Unix(), 10), true
	case "$isoTimestamp":
		return now().UTC().Format(time.RFC3339), true
	case "$randomInt":
		return randomInt(args), true
	case "$datetime":
		return datetime(args), true
	case "$processEnv":
		if len(args) == 0 {
			return "", true
		}
		return os.Getenv(args[0]), true
	default:
		return "", false
	}
}

// randomInt implements {{$randomInt [min max]}} returning a value in [min, max).
// With no args it defaults to [0, 1000). Malformed args fall back to the default.
func randomInt(args []string) string {
	min, max := 0, 1000
	if len(args) >= 2 {
		a, errA := strconv.Atoi(args[0])
		b, errB := strconv.Atoi(args[1])
		if errA == nil && errB == nil {
			min, max = a, b
		}
	}
	if max <= min {
		return strconv.Itoa(min)
	}
	return strconv.Itoa(min + randIntn(max-min))
}

// datetime implements {{$datetime <fmt>}} for the named formats rfc1123 and
// iso8601. An unknown or missing format yields RFC 3339 UTC.
func datetime(args []string) string {
	t := now().UTC()
	if len(args) == 0 {
		return t.Format(time.RFC3339)
	}
	switch strings.ToLower(args[0]) {
	case "rfc1123":
		return t.Format(time.RFC1123)
	case "iso8601":
		return t.Format(time.RFC3339)
	default:
		return t.Format(time.RFC3339)
	}
}

// uuidV4 returns a random RFC 4122 version-4 UUID string.
func uuidV4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
