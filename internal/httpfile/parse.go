// Package httpfile parses IntelliJ / VS Code "REST Client" .http files into an
// ordered list of executable steps.
package httpfile

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

var (
	separator = regexp.MustCompile(`^###`)                    // ### [optional name]
	varDef    = regexp.MustCompile(`^@([\w.-]+)\s*=\s*(.*)$`) // @host = https://...
)

// normalizeNewlines converts \r\n and lone \r line endings to \n so the rest of
// the parser, which is line-oriented on "\n", never sees a trailing carriage
// return on a value.
func normalizeNewlines(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// ParseFile reads a .http file from disk and parses it, resolving any
// `# @import ./other.http` directives by splicing the imported file's steps in
// at the point of import. The supplied vars (from an environment file) are
// extended in place with the inline @definitions of every file involved.
func ParseFile(path string, vars Vars) ([]step.Step, error) {
	if vars == nil {
		vars = Vars{}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return parseFile(abs, vars, nil)
}

// parseFile reads and parses one file as part of an import chain. stack is the
// list of absolute paths currently being parsed (the import ancestry), used to
// detect cycles before reading would recurse forever.
func parseFile(path string, vars Vars, stack []string) ([]step.Step, error) {
	for _, p := range stack {
		if p == path {
			chain := append(append([]string{}, stack...), path)
			return nil, fmt.Errorf("import cycle: %s", strings.Join(chain, " → "))
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseSteps(string(data), filepath.Dir(path), vars, append(stack, path))
}

// Parse turns the contents of a .http file into steps. Inline @definitions are
// merged into vars (overriding the env file). Placeholders in the steps are NOT
// expanded here — that happens at execution time so response captures can flow
// into later steps. `@import` directives are resolved relative to the current
// working directory; use ParseFile for path-relative imports and cycle errors.
func Parse(src string, vars Vars) []step.Step {
	if vars == nil {
		vars = Vars{}
	}
	steps, _ := parseSteps(src, "", vars, nil)
	return steps
}

// parseSteps is the core parser shared by Parse and parseFile: it harvests
// inline variable definitions, then turns each ### block into a step. An
// `@import` block is replaced by the steps of the referenced file, resolved
// relative to dir and guarded against cycles via stack.
func parseSteps(src, dir string, vars Vars, stack []string) ([]step.Step, error) {
	// Normalize Windows (\r\n) and classic-Mac (\r) line endings up front so a
	// stray \r can't ride along on a URL, header value, directive arg, or
	// captured-var name in a plan authored on Windows. Every split below is on
	// "\n", so this single pass covers the whole line-oriented parser.
	src = normalizeNewlines(src)
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
		// An `@import` block contributes the imported file's steps (with their
		// own groups intact) rather than a step of its own; captures from those
		// steps then flow forward through the shared variable map at run time.
		if s.Import != "" {
			imported, err := resolveImport(s.Import, dir, vars, stack)
			if err != nil {
				return nil, err
			}
			steps = append(steps, imported...)
			continue
		}
		if !ok {
			continue
		}
		if s.Group == "" {
			s.Group = current
		}
		steps = append(steps, s)
	}
	return steps, nil
}

// resolveImport loads the steps of an imported plan. The reference is resolved
// relative to the importing file's directory (dir); nested imports inside it
// resolve relative to their own file, matching the `< body` file rule.
func resolveImport(ref, dir string, vars Vars, stack []string) ([]step.Step, error) {
	path := ref
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return parseFile(path, vars, stack)
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
	case strings.HasPrefix(directive, "@import"):
		s.Import = strings.TrimSpace(strings.TrimPrefix(directive, "@import"))
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

// parseBodyRef reads an IntelliJ "input file" body line: `< path` sends the
// file verbatim, `<@ path` expands {{vars}} in its contents, and `<@encoding
// path` carries an encoding token that we accept and ignore (treated as UTF-8).
// It returns ok=false for a `< {% … %}` pre-request script or an empty
// reference, which the caller leaves as no body.
func parseBodyRef(line string) (path string, expand bool, ok bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "<"))
	if strings.HasPrefix(rest, "@") {
		expand = true
		rest = strings.TrimSpace(rest[1:])
	}
	if rest == "" || strings.HasPrefix(rest, "{%") {
		return "", false, false // pre-request script or empty reference
	}
	fields := strings.Fields(rest)
	// `<@encoding path` puts the encoding first; take the last token as the path.
	if expand && len(fields) > 1 {
		return fields[len(fields)-1], true, true
	}
	return fields[0], expand, true
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

	// Remainder is the request body. A leading `>` is a response-handler script
	// and a `< {% … %}` is a pre-request script — both still ignored. A `< path`
	// / `<@ path` line names a file to send as the body; record it for the
	// executor to read (its contents replace the inline body).
	rest := strings.TrimSpace(strings.Join(lines[i:], "\n"))
	switch {
	case rest == "" || strings.HasPrefix(rest, ">"):
		// no body, or a response-handler script — nothing to send
	case strings.HasPrefix(rest, "<"):
		first := rest
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			first = rest[:nl]
		}
		if path, expand, ok := parseBodyRef(first); ok {
			s.BodyFile, s.BodyFileVars = path, expand
		}
	default:
		s.Body = rest
	}

	if s.Name == "" {
		s.Name = s.Method + " " + s.URL
	}
	return s, true
}
