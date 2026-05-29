//go:build windows

package exec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// shellCommand builds the command that runs a shell step's body on Windows.
//
// Resolution order:
//  1. LAZYHTTP_SHELL — an explicit override (e.g. "cmd", "powershell", "bash",
//     or a full path), so a user can force a specific interpreter.
//  2. $SHELL — set by Git Bash, MSYS2, and Cygwin (but not by native cmd or
//     PowerShell). Honoring it means a body written for POSIX sh runs through
//     bash when the user launched lazyhttp from one of those Unix-like shells,
//     the way they'd expect — same behavior as the Unix runner.
//  3. PowerShell — the default for native cmd/PowerShell sessions.
//
// Shell bodies are still not portable across interpreters; see the "Windows
// notes" section of docs/http-format.md.
func shellCommand(body string) *exec.Cmd {
	if override := os.Getenv("LAZYHTTP_SHELL"); override != "" {
		return windowsShell(override, body)
	}
	if shell := os.Getenv("SHELL"); shell != "" {
		return windowsShell(shell, body)
	}
	return exec.Command("powershell", "-NoProfile", "-Command", body)
}

// windowsShell turns a shell name/path into the right *exec.Cmd, picking the
// invocation convention from the basename: cmd uses /c, PowerShell variants use
// -NoProfile -Command, and anything else is treated as a POSIX-style shell run
// with -c (bash/sh/zsh from Git Bash, MSYS2, or Cygwin).
func windowsShell(shell, body string) *exec.Cmd {
	switch strings.TrimSuffix(strings.ToLower(filepath.Base(shell)), ".exe") {
	case "cmd":
		comspec := os.Getenv("COMSPEC")
		if comspec == "" {
			comspec = "cmd.exe"
		}
		return exec.Command(comspec, "/c", body)
	case "powershell", "pwsh":
		return exec.Command(shell, "-NoProfile", "-Command", body)
	default:
		// POSIX-style shell. $SHELL from MSYS/Cygwin is often a virtual path
		// (e.g. /usr/bin/bash) that native Windows can't resolve; if so, fall
		// back to the bare name, which is on PATH inside those environments.
		if _, err := exec.LookPath(shell); err != nil {
			if resolved, err := exec.LookPath(filepath.Base(shell)); err == nil {
				shell = resolved
			}
		}
		return exec.Command(shell, "-c", body)
	}
}
