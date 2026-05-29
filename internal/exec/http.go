package exec

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

// httpClient is shared across requests; 30s is generous for a manual runner.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// streamClient is used for `# @stream` steps. Unlike httpClient it has no overall
// timeout: Client.Timeout caps the entire body read, which would truncate a
// long-lived stream (an SSE feed, a chunked LLM completion). A streaming request
// is bounded by its context (cancellation) or an explicit `# @timeout` instead.
var streamClient = &http.Client{}

// noRedirect is the CheckRedirect that makes a client return the 3xx response
// itself instead of following the Location header (for `# @no-redirect` steps).
func noRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// clientFor returns the client to send s with. Most steps reuse the shared
// httpClient; a step carrying a per-request `# @timeout` or `# @no-redirect`
// gets a shallow copy with just those fields overridden, leaving the shared
// client (and its transport/connection pool) untouched.
func clientFor(s step.Step) *http.Client {
	base := httpClient
	if s.Stream {
		// A stream must not be cut off by the shared 30s deadline; start from the
		// no-timeout client and let only an explicit `# @timeout` re-impose one.
		base = streamClient
	}
	if s.Timeout == 0 && !s.NoRedirect && base == httpClient {
		return httpClient
	}
	c := *base
	if s.Timeout > 0 {
		c.Timeout = s.Timeout
	}
	if s.NoRedirect {
		c.CheckRedirect = noRedirect
	}
	return &c
}

// doHTTP builds and sends the request described by s, returning its Result. When
// auth is non-nil it first resolves any {{$auth.token(...)}} placeholders
// (fetching/caching a token); a token failure fails the step like a transport
// error rather than sending an unauthenticated request.
func doHTTP(s step.Step, auth AuthResolver) step.Result {
	start := time.Now()
	fail := func(err error) step.Result {
		return step.Result{Status: step.Failed, Err: err, Duration: time.Since(start)}
	}

	if auth != nil {
		if err := auth.Resolve(&s); err != nil {
			return fail(err)
		}
	}

	var bodyReader io.Reader
	if s.Body != "" {
		bodyReader = strings.NewReader(s.Body)
	}
	req, err := http.NewRequest(s.Method, s.URL, bodyReader)
	if err != nil {
		return fail(err)
	}
	for k, v := range s.Headers {
		if strings.EqualFold(k, "Authorization") {
			v = encodeBasicAuth(v)
		}
		req.Header.Set(k, v)
	}

	resp, err := clientFor(s).Do(req)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fail(err)
	}

	// A streamed body is transformed (or kept raw) exactly as the live TUI path
	// does — never run through prettyJSON, which would mangle the event framing —
	// so headless captures/assertions evaluate against identical text.
	out := prettyJSON(resp.Header.Get("Content-Type"), body)
	if s.Stream {
		out, err = applyStreamTransform(s, body)
		if err != nil {
			return fail(err)
		}
	}

	return step.Result{
		Status:     step.Done,
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       out,
		Duration:   time.Since(start),
		NoRedirect: s.NoRedirect,
	}
}
