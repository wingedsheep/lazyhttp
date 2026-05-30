package exec

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

// doShell executes the step's body via the user's shell, capturing combined
// stdout+stderr and the exit code into a Result. The shell itself is chosen
// per-OS by shellCommand (see shell_unix.go / shell_windows.go); everything else
// here — timing, output capture, exit-code handling — is platform-neutral.
func doShell(s step.Step) step.Result {
	start := time.Now()

	cmd := shellCommand(s.Body)
	cmd.Env = os.Environ()
	var out limitedBuffer
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
	return res
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
