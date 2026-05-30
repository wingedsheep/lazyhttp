package exec

import (
	"io"
	"strings"
	"testing"
)

// TestReadLimited checks the bounded reader returns short input verbatim and
// clips anything past the cap, flagging the truncation.
func TestReadLimited(t *testing.T) {
	body, truncated, err := readLimited(strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("readLimited: %v", err)
	}
	if string(body) != "hello" || truncated {
		t.Errorf("under cap: got %q truncated=%v, want %q false", body, truncated, "hello")
	}

	big := strings.NewReader(strings.Repeat("x", maxBodyBytes+100))
	body, truncated, err = readLimited(big)
	if err != nil {
		t.Fatalf("readLimited (big): %v", err)
	}
	if len(body) != maxBodyBytes || !truncated {
		t.Errorf("over cap: got len=%d truncated=%v, want len=%d true", len(body), truncated, maxBodyBytes)
	}
}

// TestLimitedBuffer checks the writer accumulates up to the cap, drops the rest
// without reporting a short write, and appends the truncation notice.
func TestLimitedBuffer(t *testing.T) {
	var w limitedBuffer
	if _, err := io.WriteString(&w, "small"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if w.truncated || w.String() != "small" {
		t.Errorf("under cap: got %q truncated=%v, want %q false", w.String(), w.truncated, "small")
	}

	var big limitedBuffer
	n, err := big.Write([]byte(strings.Repeat("y", maxBodyBytes+50)))
	if err != nil || n != maxBodyBytes+50 {
		t.Fatalf("write must report the full slice as written, got n=%d err=%v", n, err)
	}
	if !big.truncated {
		t.Error("expected truncated flag after writing past the cap")
	}
	if big.buf.Len() != maxBodyBytes {
		t.Errorf("buffered %d bytes, want %d", big.buf.Len(), maxBodyBytes)
	}
	if !strings.HasSuffix(big.String(), truncationNotice) {
		t.Error("String() should append the truncation notice when truncated")
	}
}
