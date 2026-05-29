package exec

import (
	"io"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

// httpClient is shared across requests; 30s is generous for a manual runner.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// runHTTP builds and sends the request described by s.
func runHTTP(index int, s step.Step) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		fail := func(err error) tea.Msg {
			return ResultMsg{index, step.Result{
				Status:   step.Failed,
				Err:      err,
				Duration: time.Since(start),
			}}
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

		return ResultMsg{index, step.Result{
			Status:     step.Done,
			StatusCode: resp.StatusCode,
			Header:     resp.Header,
			Body:       prettyJSON(resp.Header.Get("Content-Type"), body),
			Duration:   time.Since(start),
		}}
	}
}
