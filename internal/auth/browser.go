package auth

import (
	"fmt"
	"os/exec"
	"runtime"
)

// openBrowser opens target in the user's default browser by shelling out to the
// platform launcher — `open` on macOS, `start` (via cmd) on Windows, and the
// first available of xdg-open / x-www-browser / www-browser elsewhere. Like
// internal/clipboard it avoids a third-party dependency: lazyhttp already shells
// out for shell steps and clipboard access, so a small exec wrapper fits.
func openBrowser(target string) error {
	switch runtime.GOOS {
	case "darwin":
		return run("open", target)
	case "windows":
		// `start` is a cmd builtin; the empty "" is the window title so the URL
		// isn't mistaken for one.
		return run("cmd", "/c", "start", "", target)
	default:
		for _, b := range []string{"xdg-open", "x-www-browser", "www-browser"} {
			if _, err := exec.LookPath(b); err == nil {
				return run(b, target)
			}
		}
		return fmt.Errorf("no browser launcher found (install xdg-open)")
	}
}

// run starts name with args, returning any spawn/exit error.
func run(name string, args ...string) error {
	if err := exec.Command(name, args...).Start(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
