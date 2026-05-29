// Package clipboard writes text to the system clipboard by shelling out to the
// platform's clipboard tool — pbcopy on macOS, clip on Windows, and the first
// available of wl-copy / xclip / xsel on Linux/BSD. It avoids a third-party
// dependency: lazyhttp already runs shell commands (internal/exec), so a small
// exec wrapper fits the codebase and keeps the binary self-contained.
package clipboard

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// command returns the clipboard-write command and its arguments — the tool reads
// the text to copy from stdin. ok is false when no supported tool is present.
func command() (name string, args []string, ok bool) {
	switch runtime.GOOS {
	case "darwin":
		return "pbcopy", nil, true
	case "windows":
		return "clip", nil, true
	default:
		for _, c := range [][]string{
			{"wl-copy"},
			{"xclip", "-selection", "clipboard"},
			{"xsel", "--clipboard", "--input"},
		} {
			if _, err := exec.LookPath(c[0]); err == nil {
				return c[0], c[1:], true
			}
		}
	}
	return "", nil, false
}

// Copy writes text to the system clipboard, returning an error when no clipboard
// tool is available or the tool itself fails.
func Copy(text string) error {
	name, args, ok := command()
	if !ok {
		return fmt.Errorf("no clipboard tool found (install xclip, xsel, or wl-clipboard)")
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// Available reports whether a clipboard tool exists, so the UI can hint at the
// copy keys only when they'd actually work.
func Available() bool {
	_, _, ok := command()
	return ok
}
