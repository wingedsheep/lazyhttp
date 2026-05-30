package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/wingedsheep/lazyhttp/internal/capture"
	"github.com/wingedsheep/lazyhttp/internal/step"
)

// Streaming breaks exec's one-shot assumption: a normal step yields exactly one
// ResultMsg, but a `# @stream` step delivers many messages over the life of one
// request. RunStream connects (off the UI thread) and starts a pump goroutine
// that reads the response body in slices; each slice arrives as a StreamChunkMsg
// and the close as a terminal ResultMsg. The UI keeps the flow going by returning
// WaitForChunk after every chunk — the standard Bubble Tea "long-running command
// pumping messages" pattern. The same terminal ResultMsg the buffered path
// produces means captures/assertions and the chain logic need no special case.

// StreamStartMsg is the first message RunStream emits once the response headers
// are in: it hands the UI the StreamSub so the model can hold it (to wait for
// chunks and to Cancel on navigate-away). The UI returns WaitForChunk(Sub).
type StreamStartMsg struct {
	Index int
	Sub   *StreamSub
}

// StreamChunkMsg carries one incremental slice of a streaming response body.
// After handling it the UI returns WaitForChunk(Sub) to pull the next message.
type StreamChunkMsg struct {
	Index int
	Data  string
	Sub   *StreamSub
}

// StreamDoneMsg ends a stream that was cancelled (the user navigated away,
// reloaded, or started another run). The accumulated result is intentionally
// dropped — whoever cancelled already updated the step's state — so the UI just
// clears its subscription handle. Sub identifies which stream finished, so a
// late StreamDoneMsg doesn't clear a newer stream's subscription.
type StreamDoneMsg struct {
	Index int
	Sub   *StreamSub
}

// StreamSub is a live subscription to a streaming response. It is opaque to the
// UI: hold the one delivered by StreamStartMsg / StreamChunkMsg and pass it to
// WaitForChunk to pull the next message; call Cancel to abort.
type StreamSub struct {
	index     int
	events    <-chan streamEvent
	cancel    context.CancelFunc
	cancelled int32 // atomic; set by Cancel so WaitForChunk drops late events
}

// Cancel aborts the in-flight request and tells WaitForChunk to discard whatever
// is still buffered instead of delivering it. Safe to call more than once. The
// pump goroutine still drains to completion (WaitForChunk keeps reading), so
// nothing leaks.
func (s *StreamSub) Cancel() {
	if s == nil {
		return
	}
	atomic.StoreInt32(&s.cancelled, 1)
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *StreamSub) isCancelled() bool { return atomic.LoadInt32(&s.cancelled) == 1 }

// streamEvent is one item from the pump goroutine: a body slice (done == false)
// or the terminal event carrying the finished Result (done == true).
type streamEvent struct {
	data   string
	done   bool
	result step.Result
}

// RunStream sends s as a streaming request and returns a command that performs
// the connect off the UI thread, then emits a StreamStartMsg. A connect/auth
// failure short-circuits to a failed ResultMsg, exactly like Run, so the caller
// handles both paths through the same result wiring.
func RunStream(index int, s step.Step, auth AuthResolver) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		fail := func(err error) tea.Msg {
			return ResultMsg{Index: index, Result: step.Result{
				Status: step.Failed, Err: err, Duration: time.Since(start)}}
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
		ctx, cancel := context.WithCancel(context.Background())
		req, err := http.NewRequestWithContext(ctx, s.Method, s.URL, bodyReader)
		if err != nil {
			cancel()
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
			cancel()
			return fail(err)
		}

		events := make(chan streamEvent, 32)
		if s.StreamThrough != "" {
			go pumpThrough(ctx, resp, s, start, events)
		} else {
			go pump(resp, s, start, events)
		}
		return StreamStartMsg{Index: index, Sub: &StreamSub{
			index: index, events: events, cancel: cancel}}
	}
}

// pump reads resp.Body in slices, sending each as a chunk event and a final
// terminal event with the accumulated Result when the stream ends (EOF, a read
// error, or context cancellation). It always closes resp.Body and the events
// channel, so WaitForChunk's loop terminates and the goroutine never leaks.
func pump(resp *http.Response, s step.Step, start time.Time, events chan<- streamEvent) {
	defer close(events)
	defer resp.Body.Close()

	var acc strings.Builder
	var ext *sseExtractor
	if s.StreamExtract != "" {
		ext = &sseExtractor{path: s.StreamExtract}
	}
	emit := func(out string) {
		if out == "" {
			return
		}
		acc.WriteString(out)
		events <- streamEvent{data: out}
	}

	buf := make([]byte, 4096)
	var readErr error
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			if ext != nil {
				// Distil each SSE frame down to the requested field; raw framing
				// (keepalive comments, the data: envelope, [DONE]) is dropped.
				chunk = ext.feed(chunk)
			}
			emit(chunk)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			break
		}
	}
	if ext != nil {
		// Flush any field left in a trailing line that had no closing newline.
		emit(ext.flush())
	}

	res := step.Result{
		Status:     step.Done,
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       acc.String(),
		Duration:   time.Since(start),
		NoRedirect: s.NoRedirect,
	}
	// A context cancellation is a deliberate stop, not a failure — keep whatever
	// streamed in so it stays viewable; WaitForChunk drops it for a cancelled sub
	// anyway. Any other read error fails the step like a transport error.
	if readErr != nil && !errors.Is(readErr, context.Canceled) {
		res.Status = step.Failed
		res.Err = readErr
	}
	events <- streamEvent{done: true, result: res}
}

// WaitForChunk returns a command that blocks until the next event from sub
// arrives, yielding it as a StreamChunkMsg (more to come) or a terminal
// ResultMsg (the stream closed). After Cancel it instead drains every remaining
// event — letting the pump goroutine finish — and returns a StreamDoneMsg. The
// UI returns this command after each StreamStartMsg / StreamChunkMsg.
func WaitForChunk(sub *StreamSub) tea.Cmd {
	return func() tea.Msg {
		for {
			ev, ok := <-sub.events
			if !ok {
				return StreamDoneMsg{Index: sub.index, Sub: sub}
			}
			if sub.isCancelled() {
				if ev.done {
					return StreamDoneMsg{Index: sub.index, Sub: sub}
				}
				continue // drain quietly until the terminal event
			}
			if ev.done {
				return ResultMsg{Index: sub.index, Result: ev.result}
			}
			return StreamChunkMsg{Index: sub.index, Data: ev.data, Sub: sub}
		}
	}
}

// sseExtractor turns a raw Server-Sent Events byte stream into just the value at
// a JSON path inside each `data:` frame — e.g. choices[0].delta.content for an
// OpenAI-style chat completion, so the response reads as the assembled text
// instead of the wire framing. It buffers across reads, so a frame split mid-line
// at a read boundary is handled.
type sseExtractor struct {
	path string
	buf  strings.Builder // an incomplete trailing line carried between feeds
}

// feed processes whatever complete lines chunk completes, returning the
// extracted text to emit and retaining any trailing partial line for next time.
func (e *sseExtractor) feed(chunk string) string {
	e.buf.WriteString(chunk)
	data := e.buf.String()
	nl := strings.LastIndexByte(data, '\n')
	if nl < 0 {
		return "" // no complete line yet
	}
	complete, rest := data[:nl], data[nl+1:]
	e.buf.Reset()
	e.buf.WriteString(rest)

	var out strings.Builder
	for _, line := range strings.Split(complete, "\n") {
		out.WriteString(e.line(line))
	}
	return out.String()
}

// flush extracts from any buffered line left without a closing newline when the
// stream ends.
func (e *sseExtractor) flush() string {
	if e.buf.Len() == 0 {
		return ""
	}
	line := e.buf.String()
	e.buf.Reset()
	return e.line(line)
}

// line extracts the configured field from a single SSE line. Non-`data:` lines
// (comments like `: keep-alive`, blank separators, other fields) and the
// `[DONE]` sentinel yield nothing; a `data:` payload is parsed as JSON and the
// path resolved via the same evaluator captures/asserts use.
func (e *sseExtractor) line(line string) string {
	line = strings.TrimRight(line, "\r")
	if !strings.HasPrefix(line, "data:") {
		return ""
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" || payload == "[DONE]" {
		return ""
	}
	if v, ok := capture.Eval(e.path, step.Result{Body: payload}); ok {
		return v
	}
	return ""
}

// pumpThrough streams the response through an external command: the raw body is
// copied to the command's stdin as it arrives, and the command's stdout is read
// in slices and emitted as chunks (and accumulated as the body for
// captures/asserts). It mirrors pump's lifecycle — always closing the body and
// the events channel, sending a terminal event — so WaitForChunk terminates and
// nothing leaks. ctx (the request's) is watched so a cancel kills the command;
// every send is guarded on ctx so a cancel that kills the command but leaves its
// stdout open can't wedge this goroutine on a full events channel once
// WaitForChunk has stopped draining.
func pumpThrough(ctx context.Context, resp *http.Response, s step.Step, start time.Time, events chan<- streamEvent) {
	defer close(events)
	defer resp.Body.Close()

	// send delivers an event unless the request is cancelled first, in which case
	// it abandons the send and reports false. The deferred close(events) then ends
	// the stream for WaitForChunk, so a dropped event never strands the consumer.
	send := func(ev streamEvent) bool {
		select {
		case events <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	fail := func(err error) {
		send(streamEvent{done: true, result: step.Result{
			Status: step.Failed, StatusCode: resp.StatusCode, Header: resp.Header,
			Err: err, Duration: time.Since(start), NoRedirect: s.NoRedirect}})
	}

	cmd := shellCommand(s.StreamThrough)
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fail(err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fail(err)
		return
	}
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		fail(err)
		return
	}

	// Kill the command if the request context is cancelled (user navigated away,
	// reloaded, cleared) so it can't outlive the stream.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
		case <-stop:
		}
	}()

	// Feed the response body to the command. Closing stdin at the end lets a
	// filter like jq flush and exit. Errors here surface via the command's own
	// exit/output, so they're not separately reported.
	go func() {
		defer stdin.Close()
		_, _ = io.Copy(stdin, resp.Body)
	}()

	var acc strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			acc.WriteString(chunk)
			if !send(streamEvent{data: chunk}) {
				break // cancelled — stop pumping; the deferred close ends the stream
			}
		}
		if err != nil {
			break // EOF when the command closes its stdout
		}
	}

	res := step.Result{
		Status:     step.Done,
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       acc.String(),
		Duration:   time.Since(start),
		NoRedirect: s.NoRedirect,
	}
	// A non-zero exit (or spawn failure) from the transform fails the step, with
	// the command's stderr as the reason — but not when the kill was our own
	// doing on a deliberate cancel. cmd.Wait always runs so the process is reaped.
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		res.Status = step.Failed
		res.Err = transformError(err, errBuf.String())
	}
	send(streamEvent{done: true, result: res})
}

// applyStreamTransform converts a fully-buffered stream body the same way the
// live pump does, so headless `lazyhttp run` evaluates captures/assertions
// against the identical text the TUI shows: piped through `@stream-through`,
// distilled by `@stream <jsonpath>`, or kept raw.
func applyStreamTransform(s step.Step, body []byte) (string, error) {
	switch {
	case s.StreamThrough != "":
		return runThrough(s.StreamThrough, body)
	case s.StreamExtract != "":
		ext := &sseExtractor{path: s.StreamExtract}
		return ext.feed(string(body)) + ext.flush(), nil
	default:
		return string(body), nil
	}
}

// runThrough pipes input through the shell command once, synchronously, for the
// headless path; the streaming TUI uses pumpThrough instead.
func runThrough(command string, input []byte) (string, error) {
	cmd := shellCommand(command)
	cmd.Env = os.Environ()
	cmd.Stdin = bytes.NewReader(input)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", transformError(err, errBuf.String())
	}
	return out.String(), nil
}

// transformError builds a step error from a failed `@stream-through` command,
// preferring the command's stderr (the actionable message) over the bare exit
// status when there is any.
func transformError(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr != "" {
		return fmt.Errorf("@stream-through: %s", stderr)
	}
	return fmt.Errorf("@stream-through: %w", err)
}
