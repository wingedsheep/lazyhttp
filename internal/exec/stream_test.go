package exec

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

// endlessBody is a response body that yields bytes forever, so a stream-through
// command echoing it keeps the events channel pegged full — the setup the leak
// test below needs to force the pump to block mid-send.
type endlessBody struct{}

func (endlessBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'y'
	}
	return len(p), nil
}
func (endlessBody) Close() error { return nil }

// TestPumpThroughCancelDoesNotLeak is the regression guard for the cancel leak:
// with the events channel full and nobody draining it, abandoning the
// subscription (closing gone, as Cancel does) must still let pumpThrough return.
// Before the guarded send, the read loop sat blocked on a full channel forever
// (the kill only stops the command, not the send), leaking the goroutine. We
// deliberately never read events, so the only way to observe completion is the
// goroutine returning.
func TestPumpThroughCancelDoesNotLeak(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell pipeline")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: endlessBody{}}
	events := make(chan streamEvent, 32)
	gone := make(chan struct{})
	finished := make(chan struct{})
	// `cat` echoes the endless body straight back, filling stdout faster than the
	// (unread) channel can take it.
	go func() {
		pumpThrough(ctx, resp, step.Step{StreamThrough: "cat"}, time.Now(), events, gone)
		close(finished)
	}()

	// Wait until the channel is full, i.e. the pump is blocked on a send.
	for i := 0; len(events) < cap(events); i++ {
		if i > 200 {
			t.Fatal("events channel never filled — pump did not start sending")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Abandon the subscription the way Cancel does, then cancel the request.
	close(gone)
	cancel()
	select {
	case <-finished:
		// Good: the guarded send unblocked on gone and the goroutine returned.
	case <-time.After(3 * time.Second):
		t.Fatal("pumpThrough did not return after Cancel with a full channel — goroutine leaked")
	}
}

// flushWriter wraps a ResponseWriter to flush after every write, so the test
// client sees chunks arrive incrementally rather than all at once on close.
func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// drain walks the streaming command's message chain — StreamStartMsg, then a
// WaitForChunk loop — collecting every chunk's data until the terminal message,
// which it returns alongside the accumulated chunk text.
func drain(t *testing.T, cmd tea.Cmd) (chunks string, terminal tea.Msg) {
	t.Helper()
	msg := cmd()
	start, ok := msg.(StreamStartMsg)
	if !ok {
		// A connect/auth failure short-circuits to a ResultMsg, like Run.
		return "", msg
	}
	sub := start.Sub
	var b strings.Builder
	for {
		switch m := WaitForChunk(sub)().(type) {
		case StreamChunkMsg:
			b.WriteString(m.Data)
		default:
			return b.String(), m
		}
	}
}

// TestRunStream checks that a streaming response is delivered as a sequence of
// chunks and a terminal ResultMsg whose body is the full accumulated text.
func TestRunStream(t *testing.T) {
	frames := []string{
		"data: {\"choice\":\"hel\"}\n\n",
		"data: {\"choice\":\"lo\"}\n\n",
		"data: [DONE]\n\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, f := range frames {
			fmt.Fprint(w, f)
			flush(w)
		}
	}))
	defer srv.Close()

	cmd := RunStream(0, step.Step{Kind: step.KindHTTP, Method: "GET", URL: srv.URL, Stream: true}, nil)
	chunks, terminal := drain(t, cmd)

	res, ok := terminal.(ResultMsg)
	if !ok {
		t.Fatalf("expected terminal ResultMsg, got %T", terminal)
	}
	want := strings.Join(frames, "")
	if res.Result.Body != want {
		t.Errorf("terminal body: want %q, got %q", want, res.Result.Body)
	}
	if chunks != want {
		t.Errorf("accumulated chunks: want %q, got %q", want, chunks)
	}
	if res.Result.StatusCode != http.StatusOK {
		t.Errorf("status: want 200, got %d", res.Result.StatusCode)
	}
	if !res.Result.OK() {
		t.Errorf("expected OK result, got err=%v", res.Result.Err)
	}
	// A streamed body is kept raw — never run through prettyJSON.
	if strings.Contains(res.Result.Body, "  \"choice\"") {
		t.Errorf("streamed body should not be pretty-printed: %q", res.Result.Body)
	}
}

// sseFrames is an OpenAI/OpenRouter-style chat-completion SSE body: a keepalive
// comment, three content deltas, and the [DONE] sentinel.
var sseFrames = []string{
	": KEEPALIVE\n\n",
	"data: {\"choices\":[{\"delta\":{\"content\":\"cave\"}}]}\n\n",
	"data: {\"choices\":[{\"delta\":{\"content\":\"man \"}}]}\n\n",
	"data: {\"choices\":[{\"delta\":{\"content\":\"talk\"}}]}\n\n",
	"data: [DONE]\n\n",
}

// TestSSEExtractor checks the built-in extractor distils data: frames to one
// field, ignores comments / [DONE], and handles a frame split across feeds.
func TestSSEExtractor(t *testing.T) {
	e := &sseExtractor{path: "choices[0].delta.content"}
	var got strings.Builder
	whole := strings.Join(sseFrames, "")
	// Feed it one byte at a time to exercise the cross-read line buffering.
	for _, b := range []byte(whole) {
		got.WriteString(e.feed(string(b)))
	}
	got.WriteString(e.flush())
	if got.String() != "caveman talk" {
		t.Errorf("extracted %q, want %q", got.String(), "caveman talk")
	}
}

// TestRunStreamExtract checks the extract path end to end: the chunks and the
// terminal body are the assembled field text, not the wire framing.
func TestRunStreamExtract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, f := range sseFrames {
			fmt.Fprint(w, f)
			flush(w)
		}
	}))
	defer srv.Close()

	s := step.Step{Kind: step.KindHTTP, Method: "GET", URL: srv.URL, Stream: true,
		StreamExtract: "choices[0].delta.content"}
	chunks, terminal := drain(t, RunStream(0, s, nil))

	res, ok := terminal.(ResultMsg)
	if !ok {
		t.Fatalf("expected terminal ResultMsg, got %T", terminal)
	}
	if res.Result.Body != "caveman talk" {
		t.Errorf("terminal body: want %q, got %q", "caveman talk", res.Result.Body)
	}
	if chunks != "caveman talk" {
		t.Errorf("accumulated chunks: want %q, got %q", "caveman talk", chunks)
	}

	// The headless path must produce the identical text.
	if got := Do(s, nil); got.Body != "caveman talk" {
		t.Errorf("headless Do body: want %q, got %q", "caveman talk", got.Body)
	}
}

// TestRunStreamThrough checks the external-command pipe: the live stream is fed
// to a shell command whose stdout becomes the response.
func TestRunStreamThrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell pipeline")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: alpha\n")
		flush(w)
		fmt.Fprint(w, "data: beta\n")
		flush(w)
	}))
	defer srv.Close()

	// Strip the `data: ` prefix and upper-case, so we can tell the transform ran.
	s := step.Step{Kind: step.KindHTTP, Method: "GET", URL: srv.URL, Stream: true,
		StreamThrough: "sed 's/^data: //' | tr a-z A-Z"}
	_, terminal := drain(t, RunStream(0, s, nil))

	res, ok := terminal.(ResultMsg)
	if !ok {
		t.Fatalf("expected terminal ResultMsg, got %T", terminal)
	}
	want := "ALPHA\nBETA\n"
	if res.Result.Body != want {
		t.Errorf("terminal body: want %q, got %q", want, res.Result.Body)
	}
	if !res.Result.OK() {
		t.Errorf("expected OK result, got err=%v", res.Result.Err)
	}
}

// TestRunStreamThroughError fails the step when the transform command exits
// non-zero, surfacing its stderr.
func TestRunStreamThroughError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell pipeline")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "anything\n")
	}))
	defer srv.Close()

	s := step.Step{Kind: step.KindHTTP, Method: "GET", URL: srv.URL, Stream: true,
		StreamThrough: "echo boom >&2; exit 3"}
	_, terminal := drain(t, RunStream(0, s, nil))

	res := terminal.(ResultMsg)
	if res.Result.Err == nil {
		t.Fatal("expected a failed result from a non-zero transform exit")
	}
	if !strings.Contains(res.Result.Err.Error(), "boom") {
		t.Errorf("error should carry the command's stderr, got %v", res.Result.Err)
	}
}

// TestRunStreamConnectError surfaces a connect failure as a terminal ResultMsg,
// the same path Run uses, so the caller's result wiring handles both.
func TestRunStreamConnectError(t *testing.T) {
	cmd := RunStream(3, step.Step{Kind: step.KindHTTP, Method: "GET", URL: "http://127.0.0.1:0", Stream: true}, nil)
	msg := cmd()
	res, ok := msg.(ResultMsg)
	if !ok {
		t.Fatalf("expected ResultMsg on connect error, got %T", msg)
	}
	if res.Index != 3 || res.Result.Err == nil {
		t.Errorf("want failed result for index 3, got index=%d err=%v", res.Index, res.Result.Err)
	}
}

// TestRunStreamThroughCancel checks the cancel path of the stream-through pump:
// cancelling mid-stream must terminate the pump goroutine and yield a
// StreamDoneMsg even though a transform command is in the pipe. The transform
// floods stdout (so the events channel is under pressure) until killed, which is
// the case where an unguarded send could wedge the goroutine.
func TestRunStreamThroughCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell pipeline")
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "go\n")
		flush(w)
		<-release // hold the connection open until the test lets go
	}))
	defer srv.Close()
	defer close(release)

	// `yes` floods stdout forever, so the pump keeps trying to send until the
	// cancel kills it — exactly the condition the guarded send must survive.
	s := step.Step{Kind: step.KindHTTP, Method: "GET", URL: srv.URL, Stream: true,
		StreamThrough: "yes"}
	start, ok := RunStream(0, s, nil)().(StreamStartMsg)
	if !ok {
		t.Fatal("expected StreamStartMsg")
	}
	sub := start.Sub

	if _, ok := WaitForChunk(sub)().(StreamChunkMsg); !ok {
		t.Fatal("expected a first chunk before cancel")
	}

	sub.Cancel()
	done := make(chan tea.Msg, 1)
	go func() { done <- WaitForChunk(sub)() }()
	select {
	case msg := <-done:
		if _, ok := msg.(StreamDoneMsg); !ok {
			t.Errorf("after cancel, want StreamDoneMsg, got %T", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WaitForChunk did not return after cancel — pumpThrough goroutine leaked")
	}
}

// TestRunStreamCancel checks that cancelling mid-stream drains the pump
// goroutine to completion and yields a StreamDoneMsg rather than delivering the
// result — the path the TUI uses when the user reloads or clears mid-stream.
func TestRunStreamCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: first\n\n")
		flush(w)
		<-release // hold the connection open until the test lets go
	}))
	defer srv.Close()
	defer close(release)

	start, ok := RunStream(0, step.Step{Kind: step.KindHTTP, Method: "GET", URL: srv.URL, Stream: true}, nil)().(StreamStartMsg)
	if !ok {
		t.Fatal("expected StreamStartMsg")
	}
	sub := start.Sub

	// Pull the first chunk, then cancel and confirm the wait loop ends cleanly.
	first := WaitForChunk(sub)()
	if _, ok := first.(StreamChunkMsg); !ok {
		t.Fatalf("expected first StreamChunkMsg, got %T", first)
	}

	sub.Cancel()
	done := make(chan tea.Msg, 1)
	go func() { done <- WaitForChunk(sub)() }()
	select {
	case msg := <-done:
		if _, ok := msg.(StreamDoneMsg); !ok {
			t.Errorf("after cancel, want StreamDoneMsg, got %T", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForChunk did not return after cancel — pump goroutine leaked")
	}
}

// drainAfterStop keeps pulling from sub until a terminal message arrives,
// collecting any remaining chunk data, and asserts the terminal is a ResultMsg
// (Stop keeps the result) rather than a StreamDoneMsg (Cancel discards it).
func drainAfterStop(t *testing.T, sub *StreamSub) ResultMsg {
	t.Helper()
	out := make(chan tea.Msg, 1)
	go func() {
		for {
			switch m := WaitForChunk(sub)().(type) {
			case StreamChunkMsg:
				continue
			default:
				out <- m
				return
			}
		}
	}()
	select {
	case m := <-out:
		res, ok := m.(ResultMsg)
		if !ok {
			t.Fatalf("after Stop, want a terminal ResultMsg, got %T", m)
		}
		return res
	case <-time.After(3 * time.Second):
		t.Fatal("WaitForChunk did not deliver a terminal result after Stop")
		return ResultMsg{}
	}
}

// TestRunStreamStopKeepsResult checks the graceful-stop path: stopping mid-stream
// delivers the accumulated body as a normal terminal result, so what already
// arrived is kept (and captures/assertions can run) rather than discarded.
func TestRunStreamStopKeepsResult(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: first\n\n")
		flush(w)
		<-release // hold the connection open until the test lets go
	}))
	defer srv.Close()
	defer close(release)

	start := RunStream(0, step.Step{Kind: step.KindHTTP, Method: "GET", URL: srv.URL, Stream: true}, nil)().(StreamStartMsg)
	sub := start.Sub
	if _, ok := WaitForChunk(sub)().(StreamChunkMsg); !ok {
		t.Fatal("expected a first chunk before stop")
	}

	sub.Stop()
	res := drainAfterStop(t, sub)
	if !strings.Contains(res.Result.Body, "data: first") {
		t.Errorf("stopped result should keep the partial body, got %q", res.Result.Body)
	}
	if res.Result.Status == step.Failed {
		t.Errorf("a graceful stop should not fail the step: %v", res.Result.Err)
	}
}

// TestRunStreamThroughStopKeepsResult is the stream-through counterpart: a Stop
// kills the transform but still delivers what it produced as the terminal result.
func TestRunStreamThroughStopKeepsResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell pipeline")
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "alpha\n")
		flush(w)
		<-release
	}))
	defer srv.Close()
	defer close(release)

	s := step.Step{Kind: step.KindHTTP, Method: "GET", URL: srv.URL, Stream: true, StreamThrough: "cat"}
	start := RunStream(0, s, nil)().(StreamStartMsg)
	sub := start.Sub
	if _, ok := WaitForChunk(sub)().(StreamChunkMsg); !ok {
		t.Fatal("expected a first chunk before stop")
	}

	sub.Stop()
	res := drainAfterStop(t, sub)
	if !strings.Contains(res.Result.Body, "alpha") {
		t.Errorf("stopped through-result should keep the partial body, got %q", res.Result.Body)
	}
	if res.Result.Status == step.Failed {
		t.Errorf("a graceful stop should not fail the step: %v", res.Result.Err)
	}
}
