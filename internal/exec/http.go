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

	resp, err := httpClient.Do(req)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fail(err)
	}

	return step.Result{
		Status:     step.Done,
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       prettyJSON(resp.Header.Get("Content-Type"), body),
		Duration:   time.Since(start),
	}
}
