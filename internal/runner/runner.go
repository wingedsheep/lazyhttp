// Package runner owns the UI-independent execution engine: the variable
// lifecycle — parse → expand → execute → evaluate → capture — and the
// plan-level run loop. Both the TUI (internal/ui) and any headless caller drive
// a Plan, so capture/assert/reset semantics live in exactly one place rather
// than being duplicated inside the Bubble Tea model.
package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wingedsheep/lazyhttp/internal/auth"
	"github.com/wingedsheep/lazyhttp/internal/capture"
	"github.com/wingedsheep/lazyhttp/internal/exec"
	"github.com/wingedsheep/lazyhttp/internal/httpfile"
	"github.com/wingedsheep/lazyhttp/internal/step"
)

// placeholderRe matches a {{token}} template, capturing the trimmed inner token.
var placeholderRe = regexp.MustCompile(`\{\{\s*([^}]+?)\s*\}\}`)

// Unresolved returns the distinct {{var}} placeholders still present in an
// expanded step's executable fields (URL, headers, body) — variables that did
// not resolve and would otherwise be sent literally (e.g. a URL of
// "{{api}}/login", which fails with a bare "unsupported protocol scheme").
//
// Tokens filled by a later stage are deliberately excluded: anything starting
// with "$" (dynamic variables and {{$auth.token(...)}}, which the auth resolver
// substitutes inside exec) and inline response references ("name.response.…",
// resolved once their source step has run). What remains is the set of plain
// environment / @def variables that are genuinely missing, so a caller can fail
// the step with a clear, accurate message.
func Unresolved(s step.Step) []string {
	seen := map[string]bool{}
	var out []string
	scan := func(in string) {
		for _, m := range placeholderRe.FindAllStringSubmatch(in, -1) {
			tok := m[1]
			if strings.HasPrefix(tok, "$") || strings.Contains(tok, ".response.") {
				continue
			}
			if !seen[tok] {
				seen[tok] = true
				out = append(out, tok)
			}
		}
	}
	scan(s.URL)
	for _, v := range s.Headers {
		scan(v)
	}
	scan(s.Body)
	return out
}

// UnresolvedError formats the missing variables from Unresolved into a step
// error. hint is appended after an em-dash when non-empty, letting a caller add
// context it holds (e.g. the TUI's "press E to select an environment").
func UnresolvedError(missing []string, hint string) error {
	noun := "variable"
	if len(missing) > 1 {
		noun = "variables"
	}
	list := "{{" + strings.Join(missing, "}}, {{") + "}}"
	if hint == "" {
		return fmt.Errorf("unresolved %s %s", noun, list)
	}
	return fmt.Errorf("unresolved %s %s — %s", noun, list, hint)
}

// Plan is a parsed test plan together with the mutable state of running it. It
// is the single source of truth for the variable lifecycle, shared by the TUI
// and any headless caller.
//
// Vars accumulates captured values as steps run; BaseVars is the pristine
// env+inline snapshot restored on a reset. Results holds each step's outcome,
// indexed in step with Steps so an inline {{name.response...}} reference can
// look up an earlier step's result.
type Plan struct {
	Steps   []step.Step
	Results []step.Result

	// Dir is the plan file's directory, used to resolve `< file` body paths.
	Dir string

	Vars     httpfile.Vars
	BaseVars httpfile.Vars

	// AuthConfigs are the OAuth2 configurations from the environment's
	// Security.Auth block; AuthCache reuses tokens fetched from them across
	// steps until they expire.
	AuthConfigs map[string]auth.Config
	AuthCache   *auth.Cache
}

// Load reads and parses the plan at path against the named environment (which
// may be ""), returning a Plan ready to run: variables resolved, OAuth2 configs
// loaded, and a fresh (empty) result per step. A new token cache per load means
// switching credentials never reuses a stale token. A malformed Security block
// is non-fatal — it just leaves auth disabled.
func Load(path, envName string) (*Plan, error) {
	vars, err := httpfile.LoadEnv(path, envName)
	if err != nil {
		return nil, err
	}
	steps, err := httpfile.ParseFile(path, vars)
	if err != nil {
		return nil, err
	}
	authConfigs, _ := httpfile.LoadAuth(path, envName)
	return &Plan{
		Steps:       steps,
		Results:     make([]step.Result, len(steps)),
		Dir:         filepath.Dir(path),
		Vars:        vars,
		BaseVars:    cloneVars(vars),
		AuthConfigs: authConfigs,
		AuthCache:   auth.NewCache(),
	}, nil
}

// RunAll executes every step top to bottom, threading captures forward exactly
// as the TUI's run-from-here chain does: each result is evaluated (captures into
// Vars, assertions recorded), a successful @reset step clears the other results
// and drops captures back to BaseVars, and the run stops at the first step that
// is not OK (transport error, non-2xx, or a failed assertion). ctx is checked
// between steps so a caller can cancel a long plan. The returned slice is the
// Plan's Results; err is non-nil only for ctx cancellation.
func (p *Plan) RunAll(ctx context.Context) ([]step.Result, error) {
	return p.Run(ctx, nil)
}

// Run is RunAll restricted to the steps for which include(i) is true; pass nil
// to run every step (what RunAll does). A skipped step keeps its Pending result
// and takes no part in capture, @reset, or the stop-on-first-failure chain — so
// a headless `--filter` runs only the matching steps while everything else
// (expansion ordering, capture→assert→reset, stop semantics) is identical.
func (p *Plan) Run(ctx context.Context, include func(i int) bool) ([]step.Result, error) {
	for i := range p.Steps {
		if err := ctx.Err(); err != nil {
			return p.Results, err
		}
		if include != nil && !include(i) {
			continue
		}
		s, err := p.Expand(p.Steps[i])
		if err != nil {
			// A body-file read failure fails the step like a transport error.
			p.Results[i] = step.Result{Status: step.Failed, Err: err}
			break
		}
		// A variable that never resolved would send a literal "{{var}}" — fail
		// the step with a clear message rather than firing a broken request.
		if s.Kind == step.KindHTTP {
			if missing := Unresolved(s); len(missing) > 0 {
				p.Results[i] = step.Result{Status: step.Failed,
					Err: UnresolvedError(missing, "not defined in the selected environment or @vars")}
				break
			}
		}
		res := p.Evaluate(i, exec.Do(s, p.AuthResolver(s)))
		p.Results[i] = res
		if p.Steps[i].Reset && res.OK() {
			p.Reset(i)
		}
		if !res.OK() {
			break
		}
	}
	return p.Results, nil
}

// Label returns step i's display name with {{vars}} expanded — the same name the
// TUI list shows, without the leading reset glyph. Shell steps keep their raw
// name (there's no URL to template). Used for headless reporting and --filter
// matching.
func (p *Plan) Label(i int) string {
	s := p.Steps[i]
	if s.Kind == step.KindShell {
		return s.Name
	}
	return p.Vars.Expand(s.Name)
}

// Expand returns a copy of s with its URL, headers and body resolved against the
// current variables (captures included). Captures are left untouched (they
// target the response).
//
// When the step's body comes from a file (`< path` / `<@ path`), the file is
// read here — where the variable set is available — and its contents become the
// body. `<@` additionally expands {{vars}} in those contents; `<` sends them
// verbatim. The path is resolved relative to the plan file's directory (Dir). A
// read error is returned so the caller can surface it as a failed result rather
// than silently sending an empty body. BodyFile is kept on the returned step
// (now holding the var-expanded path) for the request preview.
func (p *Plan) Expand(s step.Step) (step.Step, error) {
	expand := func(in string) string { return p.Vars.ExpandFunc(in, p.ResolveResponseRef) }

	s.URL = expand(s.URL)
	headers := make(map[string]string, len(s.Headers))
	for k, v := range s.Headers {
		headers[k] = expand(v)
	}
	s.Headers = headers

	if s.BodyFile == "" {
		s.Body = expand(s.Body)
		return s, nil
	}

	path := expand(s.BodyFile)
	s.BodyFile = path
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(p.Dir, full)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return s, err
	}
	body := string(data)
	if s.BodyFileVars {
		body = expand(body)
	}
	s.Body = body
	return s, nil
}

// AuthResolver returns an exec.AuthResolver for the expanded step s, or nil when
// s has no {{$auth.token(...)}} reference or no Security.Auth configurations are
// defined. Configuration values are expanded here so a client secret sourced
// from {{$processEnv …}} or another variable resolves against the live var set;
// only the token fetch itself happens later, inside exec.
func (p *Plan) AuthResolver(s step.Step) exec.AuthResolver {
	if len(p.AuthConfigs) == 0 {
		return nil
	}
	referenced := auth.References(s.URL) || auth.References(s.Body)
	for _, v := range s.Headers {
		if referenced {
			break
		}
		referenced = auth.References(v)
	}
	if !referenced {
		return nil
	}

	expand := func(in string) string { return p.Vars.ExpandFunc(in, p.ResolveResponseRef) }
	cfgs := make(map[string]auth.Config, len(p.AuthConfigs))
	for id, c := range p.AuthConfigs {
		c.TokenURL = expand(c.TokenURL)
		c.AuthURL = expand(c.AuthURL)
		c.ClientID = expand(c.ClientID)
		c.ClientSecret = expand(c.ClientSecret)
		c.Scope = expand(c.Scope)
		c.Username = expand(c.Username)
		c.Password = expand(c.Password)
		cfgs[id] = c
	}
	return auth.NewResolver(cfgs, p.AuthCache)
}

// ResolveResponseRef resolves an inline response reference — VS Code REST Client
// syntax such as {{login.response.body.$.token}} or
// {{login.response.headers.Location}} — against the stored result of an earlier
// named step. It maps the reference onto a capture expression and reuses
// capture.Eval, so JSON paths and header lookups behave exactly as in
// `# @capture`. ok is false for tokens that aren't response references, name an
// unrun step, or can't be resolved, so Expand leaves them untouched.
func (p *Plan) ResolveResponseRef(token string) (string, bool) {
	name, rest, ok := strings.Cut(token, ".response.")
	if !ok {
		return "", false
	}
	r, ok := p.LastResult(name)
	if !ok {
		return "", false
	}
	var expr string
	switch {
	case rest == "body" || rest == "body.*":
		expr = "body"
	case strings.HasPrefix(rest, "body."):
		expr = strings.TrimPrefix(rest, "body.") // e.g. "$.token", "items[0].id"
	case strings.HasPrefix(rest, "headers."):
		expr = "header." + strings.TrimPrefix(rest, "headers.")
	default:
		return "", false
	}
	return capture.Eval(expr, r)
}

// LastResult returns the result of the most recently positioned step named name
// that has already run. Scanning from the bottom means a reference picks up the
// latest result when a name is reused across the plan.
func (p *Plan) LastResult(name string) (step.Result, bool) {
	for i := len(p.Steps) - 1; i >= 0; i-- {
		if p.Steps[i].Name == name && i < len(p.Results) && p.Results[i].Status != step.Pending {
			return p.Results[i], true
		}
	}
	return step.Result{}, false
}

// Evaluate runs a finished step's captures and assertions, returning the result
// enriched with assertion outcomes. Captures populate the variable set so later
// steps can reference them.
func (p *Plan) Evaluate(i int, r step.Result) step.Result {
	if r.Err != nil {
		return r
	}
	for _, c := range p.Steps[i].Captures {
		if val, ok := capture.Eval(c.Expr, r); ok {
			p.Vars[c.Name] = val
		}
	}
	for _, a := range p.Steps[i].Asserts {
		// Expand {{vars}} on the right-hand side so an assertion can compare the
		// response against a captured value (e.g. `json.id == {{newId}}`). Runs
		// after captures above, so a value captured by this same step is visible.
		// Want's original template survives in a.Raw, which is what the UI shows.
		a.Want = p.Vars.ExpandFunc(a.Want, p.ResolveResponseRef)
		r.Asserts = append(r.Asserts, capture.Check(a, r))
	}
	return r
}

// Reset clears every step's result except keepIdx (pass -1 to clear all) and
// drops captured variables back to the env+inline baseline, mirroring a backend
// reset that a just-run @reset step performed.
func (p *Plan) Reset(keepIdx int) {
	for i := range p.Results {
		if i != keepIdx {
			p.Results[i] = step.Result{}
		}
	}
	p.Vars = cloneVars(p.BaseVars)
}

// cloneVars returns an independent copy of a variable set.
func cloneVars(v httpfile.Vars) httpfile.Vars {
	out := make(httpfile.Vars, len(v))
	for k, val := range v {
		out[k] = val
	}
	return out
}
