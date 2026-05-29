//go:build !windows

package exec

import (
	"os"
	"os/exec"
)

// shellCommand builds the command that runs a shell step's body on Unix-like
// systems: an explicit LAZYHTTP_SHELL override, else the user's $SHELL (so it
// matches their login shell), falling back to /bin/sh when neither is set.
func shellCommand(body string) *exec.Cmd {
	shell := os.Getenv("LAZYHTTP_SHELL")
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		shell = "/bin/sh"
	}
	return exec.Command(shell, "-c", body)
}
