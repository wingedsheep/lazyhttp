// Package httpfile parses IntelliJ / VS Code "REST Client" .http files into an
// ordered list of executable steps.
package httpfile

import (
	"bufio"
	"os"
	"regexp"
	"strings"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

var (
	separator = regexp.MustCompile(`^###`)                    // ### [optional name]
	varDef    = regexp.MustCompile(`^@([\w.-]+)\s*=\s*(.*)$`) // @host = https://...
)

// ParseFile reads a .http file from disk and parses it. The supplied vars (from
// an environment file) are extended in place with any inline @definitions found.
func ParseFile(path string, vars Vars) ([]step.Step, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(string(data), vars), nil
}

// Parse turns the contents of a .http file into steps. Inline @definitions are
// merged into vars (overriding the env file). Placeholders in the steps are NOT
// expanded here — that happens at execution time so response captures can flow
// into later steps.
func Parse(src string, vars Vars) []step.Step {
	if vars == nil {
		vars = Vars{}
	}
	// First pass: collect inline variable definitions so they're available no
	// matter where in the file a step references them.
	for _, line := range strings.Split(src, "\n") {
		if m := varDef.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			vars[m[1]] = vars.Expand(strings.TrimSpace(m[2]))
		}
	}

	var (
		steps   []step.Step
		current string // group carried forward to subsequent steps
	)
	for _, block := range splitBlocks(src) {
		s, ok := parseBlock(block)
		if s.Group != "" {
			current = s.Group // a @group directive moves the section forward
		}
		if !ok {
			continue
		}
		if s.Group == "" {
			s.Group = current
		}
		steps = append(steps, s)
	}
	return steps
}

// block is the raw text of one ### section plus the name carried on its header.
type block struct {
	name string
	body string
}

// splitBlocks divides the file on ### separator lines, attaching any trailing
// text on the separator as the block's tentative name.
func splitBlocks(src string) []block {
	var (
		blocks  []block
		cur     *block
		scanner = bufio.NewScanner(strings.NewReader(src))
	)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	flush := func() {
		if cur != nil && strings.TrimSpace(cur.body) != "" {
			blocks = append(blocks, *cur)
		}
		cur = nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if separator.MatchString(line) {
			flush()
			name := strings.TrimSpace(strings.TrimPrefix(line, "###"))
			cur = &block{name: name}
			continue
		}
		if cur == nil {
			cur = &block{} // content before the first ### (e.g. top-level @vars)
		}
		cur.body += line + "\n"
	}
	flush()
	return blocks
}

// parseBlock turns one block into a Step. It returns ok=false for blocks that
// hold nothing but directives (e.g. a lone # @group) or comments; such blocks
// may still carry a Group that the caller propagates forward.
func parseBlock(b block) (step.Step, bool) {
	s := step.Step{Name: b.name, Headers: map[string]string{}, Raw: strings.TrimSpace(b.body)}

	lines := strings.Split(b.body, "\n")
	i := 0

	// Leading directives and comments: # @shell, # @name, # @group, # @capture,
	// plain # comments, and @var = ... definitions (harvested elsewhere).
	for ; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		switch {
		case t == "":
			continue
		case strings.HasPrefix(t, "#"):
			applyDirective(&s, strings.TrimSpace(strings.TrimPrefix(t, "#")))
			continue
		case varDef.MatchString(t):
			continue
		}
		break // first real content line
	}

	body := strings.Join(lines[i:], "\n")
	if s.Kind == step.KindShell {
		s.Body = strings.TrimSpace(body)
		if s.Name == "" {
			s.Name = "shell"
		}
		return s, true
	}
	return parseHTTP(s, body)
}

// applyDirective interprets a single "# @..." comment line.
func applyDirective(s *step.Step, directive string) {
	switch {
	case directive == "@shell":
		s.Kind = step.KindShell
	case directive == "@reset":
		s.Reset = true
	case strings.HasPrefix(directive, "@name"):
		s.Name = strings.TrimSpace(strings.TrimPrefix(directive, "@name"))
	case strings.HasPrefix(directive, "@group"):
		s.Group = strings.TrimSpace(strings.TrimPrefix(directive, "@group"))
	case strings.HasPrefix(directive, "@capture"):
		rest := strings.TrimSpace(strings.TrimPrefix(directive, "@capture"))
		if name, expr, ok := strings.Cut(rest, "="); ok {
			s.Captures = append(s.Captures, step.Capture{
				Name: strings.TrimSpace(name),
				Expr: strings.TrimSpace(expr),
			})
		}
	case strings.HasPrefix(directive, "@assert"):
		rest := strings.TrimSpace(strings.TrimPrefix(directive, "@assert"))
		if a, ok := parseAssertion(rest); ok {
			s.Asserts = append(s.Asserts, a)
		}
	}
}

// parseAssertion reads "<expr> <op> <want>" or "<expr> exists" from a directive.
func parseAssertion(rest string) (step.Assertion, bool) {
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return step.Assertion{}, false
	}
	a := step.Assertion{Expr: fields[0], Op: fields[1], Raw: rest}
	if a.Op != "exists" {
		want := strings.TrimSpace(strings.Join(fields[2:], " "))
		a.Want = strings.Trim(want, `"'`) // tolerate quoted values
	}
	return a, true
}

// parseHTTP fills in the request line, headers and body of an HTTP step. Values
// keep their {{var}} placeholders for later expansion.
func parseHTTP(s step.Step, body string) (step.Step, bool) {
	lines := strings.Split(body, "\n")
	i := 0
	for ; i < len(lines) && strings.TrimSpace(lines[i]) == ""; i++ {
	}
	if i >= len(lines) {
		return s, false // empty block
	}

	// Request line: METHOD URL [HTTP/x]. Method is optional and defaults to GET.
	fields := strings.Fields(strings.TrimSpace(lines[i]))
	i++
	switch len(fields) {
	case 0:
		return s, false
	case 1:
		s.Method, s.URL = "GET", fields[0]
	default:
		s.Method, s.URL = strings.ToUpper(fields[0]), fields[1]
	}

	// Headers until a blank line.
	for ; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			i++
			break
		}
		if k, v, ok := strings.Cut(t, ":"); ok {
			s.Headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}

	// Remainder is the request body (response handlers / refs are ignored).
	rest := strings.TrimSpace(strings.Join(lines[i:], "\n"))
	if !strings.HasPrefix(rest, ">") && !strings.HasPrefix(rest, "<") {
		s.Body = rest
	}

	if s.Name == "" {
		s.Name = s.Method + " " + s.URL
	}
	return s, true
}
