package exec

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

// runShell executes the step's body via the user's shell, capturing combined
// stdout+stderr and the exit code.
func runShell(index int, s step.Step) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()

		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}

		cmd := exec.Command(shell, "-c", s.Body)
		cmd.Env = os.Environ()
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out

		err := cmd.Run()
		res := step.Result{
			Status:   step.Done,
			ExitCode: cmd.ProcessState.ExitCode(),
			Body:     out.String(),
			Duration: time.Since(start),
		}
		if err != nil {
			res.Status = step.Failed
			if _, isExit := err.(*exec.ExitError); !isExit {
				res.Err = err // spawn failure, not just a non-zero exit
			}
		}
		return ResultMsg{index, res}
	}
}

// prettyJSON indents a JSON body for readability; non-JSON content is returned
// unchanged. Lives here so both runners can share it.
func prettyJSON(contentType string, body []byte) string {
	if !strings.Contains(contentType, "json") {
		return string(body)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, body, "", "  "); err != nil {
		return string(body)
	}
	return buf.String()
}
