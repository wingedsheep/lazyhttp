package exec

import (
	"bytes"
	"fmt"
	"io"
)

// maxBodyBytes caps how much of a response body or shell output we buffer in
// memory. The shared client's 30s timeout bounds how long a request runs, not
// how large it is, so without a cap a huge response — or a runaway command like
// `yes` — would grow memory without bound. 32 MiB is far beyond any realistic
// test-plan response yet small enough never to threaten the process.
const maxBodyBytes = 32 << 20 // 32 MiB

// readLimited reads r fully into memory but no further than maxBodyBytes. The
// returned flag reports whether r held more than the cap, in which case the
// excess is discarded. Reading one byte past the limit is what lets us tell
// "exactly at the cap" apart from "overflowed".
func readLimited(r io.Reader) (body []byte, truncated bool, err error) {
	body, err = io.ReadAll(io.LimitReader(r, maxBodyBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(body) > maxBodyBytes {
		return body[:maxBodyBytes], true, nil
	}
	return body, false, nil
}

// limitedBuffer is an io.Writer that accumulates up to maxBodyBytes and silently
// drops the rest, recording that it truncated. It bounds memory for output that
// is streamed to us a write at a time (shell stdout/stderr), where a runaway
// command would otherwise fill an ordinary bytes.Buffer without limit.
type limitedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxBodyBytes - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
			w.truncated = true
		} else {
			w.buf.Write(p)
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	// Always report the whole slice as written: dropping the overflow is
	// deliberate, and a short write would make exec.Cmd treat it as an error.
	return len(p), nil
}

func (w *limitedBuffer) String() string {
	s := w.buf.String()
	if w.truncated {
		s += truncationNotice
	}
	return s
}

// truncationNotice is appended to a body that hit maxBodyBytes so the user sees
// the output was cut rather than silently shortened.
var truncationNotice = fmt.Sprintf("\n\n… [truncated at %d MiB]", maxBodyBytes>>20)
