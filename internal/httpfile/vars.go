package httpfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/wingedsheep/lazyhttp/internal/auth"
)

// varPattern matches IntelliJ-style placeholders: plain {{host}}, dynamic
// variables with optional args ({{$uuid}}, {{$randomInt 0 9}}), and inline
// response references ({{login.response.body.$.id}}). It captures any run of
// non-brace characters between {{ }} and lets Expand decide how to resolve it,
// so JSON-path punctuation ($ . [ ] *) inside a reference comes through intact.
var varPattern = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

// Vars holds the resolved variable set for a plan: values defined inline in the
// .http file (@name = value) layered over values from an environment file.
type Vars map[string]string

// The IntelliJ/VS Code environment files looked up alongside (and above) a
// plan. The private file holds secrets (kept out of version control) and is
// layered over the shared file, matching IntelliJ HTTP Client and VS Code REST
// Client.
const (
	envFileName        = "http-client.env.json"
	privateEnvFileName = "http-client.private.env.json"
)

// findEnvDir locates the directory supplying a plan's environments by walking up
// from the plan's own directory through its ancestors, returning the first
// directory that holds a shared or private env file. This mirrors IntelliJ HTTP
// Client and VS Code REST Client, which let a repo keep its env files at a
// common root (typically an http/ directory) while plans live in subfolders.
// The walk stops once it has inspected the directory holding a .git entry — the
// repo boundary — so it can't escape the project, and at the filesystem root. An
// empty result means no env file was found.
func findEnvDir(planPath string) string {
	dir, _ := findEnvDirTrace(planPath)
	return dir
}

// findEnvDirTrace is findEnvDir that also reports every directory it inspected,
// in walk order, so callers can tell the user where lazyhttp looked when the
// search comes up empty. The returned directory is "" when no env file was
// found; searched is non-empty regardless.
func findEnvDirTrace(planPath string) (dir string, searched []string) {
	// Absolutize first: a bare filename ("plan.http") has dir ".", and
	// filepath.Dir(".") == ".", so the walk would terminate on its first
	// iteration and never reach the real ancestor directories. Resolving against
	// the cwd gives the loop genuine parents to climb.
	if abs, err := filepath.Abs(planPath); err == nil {
		planPath = abs
	}
	dir = filepath.Dir(planPath)
	for {
		searched = append(searched, dir)
		for _, name := range []string{envFileName, privateEnvFileName} {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return dir, searched
			}
		}
		// The repo root (the directory containing .git) is the boundary: having
		// checked it for env files above, don't climb past it into unrelated
		// parent directories.
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return "", searched
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", searched // reached the filesystem root
		}
		dir = parent
	}
}

// readEnvFile reads and parses one http-client.env.json-style file into a
// per-environment map of raw JSON values. Values are left raw (rather than
// decoded to strings) so a nested object — notably the IntelliJ `Security` block
// carrying OAuth2 configurations — doesn't break decoding the way a flat
// `map[string]string` would. A missing file yields a nil map (not an error).
func readEnvFile(path string) (map[string]map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var envs map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &envs); err != nil {
		return nil, err
	}
	return envs, nil
}

// mergeEnvs overlays the private env sets onto the shared ones: for every
// environment in private, its variables override (or add to) the shared
// environment of the same name. Environments unique to either side are kept. The
// overlay is per-variable, so a private file supplying only secrets (e.g.
// clientSecret, admin_password) leaves the rest of the shared environment — and
// its Security block — intact. shared must be non-nil.
func mergeEnvs(shared, private map[string]map[string]json.RawMessage) {
	for env, vars := range private {
		if shared[env] == nil {
			shared[env] = make(map[string]json.RawMessage, len(vars))
		}
		for k, v := range vars {
			shared[env][k] = v
		}
	}
}

// loadEnvFile reads the nearest http-client.env.json — searched in the plan's
// own directory and, failing that, its ancestors (see findEnvDir) — and layers a
// http-client.private.env.json from the same directory over it (see mergeEnvs).
// A missing file yields a nil map (not an error) so a plan without environments
// simply has none.
func loadEnvFile(planPath string) (map[string]map[string]json.RawMessage, error) {
	dir := findEnvDir(planPath)
	if dir == "" {
		return nil, nil
	}
	envs, err := readEnvFile(filepath.Join(dir, envFileName))
	if err != nil {
		return nil, err
	}
	private, err := readEnvFile(filepath.Join(dir, privateEnvFileName))
	if err != nil {
		return nil, err
	}
	if len(private) == 0 {
		return envs, nil
	}
	if envs == nil {
		envs = make(map[string]map[string]json.RawMessage, len(private))
	}
	mergeEnvs(envs, private)
	return envs, nil
}

// LoadEnv reads an IntelliJ-style http-client.env.json sitting next to the plan
// and returns the variables for the named environment. Scalar values are
// rendered as strings (see scalarString) so `{{port}}` resolves whether the env
// file writes 8080 or "8080"; composite values — the `Security` OAuth2 block
// (an object, consumed by LoadAuth), arrays, and null — are skipped. A missing
// file or empty env name yields an empty (but usable) set rather than an error.
func LoadEnv(planPath, envName string) (Vars, error) {
	v := Vars{}
	if envName == "" {
		return v, nil
	}
	envs, err := loadEnvFile(planPath)
	if err != nil {
		return nil, err
	}
	for k, raw := range envs[envName] {
		if s, ok := scalarString(raw); ok {
			v[k] = s
		}
	}
	return v, nil
}

// scalarString renders a raw JSON env value as a string when it is a scalar — a
// string, number, or boolean — and reports ok=false for objects, arrays, and
// null. This lets a numeric or boolean setting (`"port": 8080`, `"debug": true`)
// fill a {{var}} instead of being silently dropped, while keeping composite
// values like the `Security` block out of the plain variable set.
func scalarString(raw json.RawMessage) (string, bool) {
	var val any
	if json.Unmarshal(raw, &val) != nil {
		return "", false
	}
	switch t := val.(type) {
	case string:
		return t, true
	case bool:
		return strconv.FormatBool(t), true
	case float64:
		// encoding/json decodes every number as float64; render it without a
		// trailing ".0" so {{port}} becomes "8080", not "8080.000000".
		return strconv.FormatFloat(t, 'f', -1, 64), true
	default:
		return "", false // object, array, or null
	}
}

// LoadAuth reads the OAuth2 configurations from the named environment's
// `Security.Auth` block in http-client.env.json. A missing file, env, or
// Security block yields a nil map (not an error). Configuration values keep
// their {{var}} placeholders for the caller to expand.
func LoadAuth(planPath, envName string) (map[string]auth.Config, error) {
	if envName == "" {
		return nil, nil
	}
	envs, err := loadEnvFile(planPath)
	if err != nil {
		return nil, err
	}
	raw, ok := envs[envName]["Security"]
	if !ok {
		return nil, nil
	}
	var sec struct {
		Auth map[string]struct {
			Type              string          `json:"Type"`
			GrantType         string          `json:"Grant Type"`
			TokenURL          string          `json:"Token URL"`
			AuthURL           string          `json:"Auth URL"`
			RedirectURL       string          `json:"Redirect URL"`
			ClientID          string          `json:"Client ID"`
			ClientSecret      string          `json:"Client Secret"`
			Scope             string          `json:"Scope"`
			Username          string          `json:"Username"`
			Password          string          `json:"Password"`
			ClientCredentials string          `json:"Client Credentials"`
			UseIDToken        bool            `json:"Use ID Token"`
			PKCE              json.RawMessage `json:"PKCE"`
		} `json:"Auth"`
	}
	if err := json.Unmarshal(raw, &sec); err != nil {
		return nil, err
	}
	if len(sec.Auth) == 0 {
		return nil, nil
	}
	out := make(map[string]auth.Config, len(sec.Auth))
	for id, a := range sec.Auth {
		out[id] = auth.Config{
			Type:              a.Type,
			GrantType:         a.GrantType,
			TokenURL:          a.TokenURL,
			AuthURL:           a.AuthURL,
			RedirectURL:       a.RedirectURL,
			ClientID:          a.ClientID,
			ClientSecret:      a.ClientSecret,
			Scope:             a.Scope,
			Username:          a.Username,
			Password:          a.Password,
			ClientCredentials: a.ClientCredentials,
			UseIDToken:        a.UseIDToken,
			PKCE:              pkceEnabled(a.PKCE),
		}
	}
	return out, nil
}

// pkceEnabled resolves the optional `PKCE` key for an Authorization Code config.
// PKCE is on by default — an absent key, an object (IntelliJ's `{"Code
// Challenge Method": "S256"}` form), or an explicit `true` all enable it; only a
// literal `false` turns it off. (It is ignored for the non-interactive grants.)
func pkceEnabled(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) != "false"
}

// LoadEnvNames returns the environment names declared in the http-client.env.json
// sitting next to the plan, sorted alphabetically. A missing file yields an empty
// slice (not an error) so a plan without environments simply has none to pick.
func LoadEnvNames(planPath string) ([]string, error) {
	envs, err := loadEnvFile(planPath)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(envs))
	for name := range envs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// EnvDiscovery records the outcome of looking for a plan's environment file:
// the directories searched (in walk order), the env file found (if any), the
// environment names it declares, and any read/parse error. It lets the UI
// explain *why* the env list is empty — wrong directory, no file in the tree, a
// malformed file, or a file with no environments — instead of failing silently.
type EnvDiscovery struct {
	Names    []string // environment names declared, sorted; empty if none resolved
	File     string   // the env file that anchored discovery, "" if none was found
	Searched []string // directories walked while looking for a file, in order
	Err      error    // a read/parse error, when a file was found but couldn't be used
}

// DiscoverEnv runs environment discovery for a plan and reports the full
// outcome. Unlike LoadEnvNames, a malformed or unreadable file is reported in
// Err (with Names left empty) rather than discarded, so the caller can surface
// it. A missing file is not an error — it leaves Names empty and File "".
func DiscoverEnv(planPath string) EnvDiscovery {
	dir, searched := findEnvDirTrace(planPath)
	d := EnvDiscovery{Searched: searched}
	if dir == "" {
		return d
	}
	// Name whichever file anchored the directory (shared preferred) for the
	// diagnostic; both may exist, but the shared file is the one users reach for.
	for _, name := range []string{envFileName, privateEnvFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			d.File = filepath.Join(dir, name)
			break
		}
	}
	envs, err := loadEnvFile(planPath)
	if err != nil {
		d.Err = err
		return d
	}
	names := make([]string, 0, len(envs))
	for name := range envs {
		names = append(names, name)
	}
	sort.Strings(names)
	d.Names = names
	return d
}

// Summary returns a one-line, human-readable explanation of why discovery
// yielded no environments — surfacing a parse error, naming a file that
// declared none, or reporting the span of directories searched in vain. It
// returns "" when environments were found, so a non-empty result reads as
// "there is something to report".
func (d EnvDiscovery) Summary() string {
	switch {
	case len(d.Names) > 0:
		return ""
	case d.Err != nil:
		return "env file error: " + d.Err.Error()
	case d.File != "":
		return "no environments declared in " + d.File
	case len(d.Searched) == 0:
		return "no environments found"
	case len(d.Searched) == 1:
		return fmt.Sprintf("no environments — searched %s for %s", d.Searched[0], envFileName)
	default:
		return fmt.Sprintf("no environments — searched %s … %s for %s",
			d.Searched[0], d.Searched[len(d.Searched)-1], envFileName)
	}
}

// Expand replaces every {{var}} in s with its resolved value. Unknown variables
// are left untouched so the user can see what failed to resolve.
func (v Vars) Expand(s string) string {
	return v.ExpandFunc(s, nil)
}

// maxExpandDepth caps how many levels of nested variable references Expand will
// follow (host → baseUrl → request). It is a runaway guard: the per-path cycle
// check below already stops self-referential definitions exactly where they
// loop, so this only bounds pathological non-cyclic fan-out.
const maxExpandDepth = 10

// ExpandFunc is Expand with an extra resolver consulted first for each
// placeholder. It lets callers that hold context the variable map can't —
// notably the UI, which resolves inline response references against stored
// results — plug that in without duplicating the matcher. resolve receives the
// trimmed token and returns ok=true to claim it; a nil resolver, or one that
// declines, falls back to dynamic variables and then the variable map, leaving
// unknown placeholders untouched.
//
// Variable-map values are expanded transitively: when {{baseUrl}} resolves to
// "{{host}}/v2", that result is rescanned so {{host}} resolves too, matching
// the composed-variable convention of IntelliJ HTTP Client and VS Code REST
// Client. Only variable-map values recurse — dynamic variables ({{$uuid}}) and
// resolver-provided values (captured response data) are inserted verbatim and
// never rescanned, so a dynamic value is evaluated exactly once and a response
// body that happens to contain literal "{{...}}" is not re-expanded.
func (v Vars) ExpandFunc(s string, resolve func(token string) (string, bool)) string {
	return v.expand(s, resolve, nil, 0)
}

// expand is the recursive core of ExpandFunc. seen is the chain of variable-map
// tokens currently being expanded, used to break reference cycles; depth bounds
// non-cyclic nesting against maxExpandDepth.
func (v Vars) expand(s string, resolve func(token string) (string, bool), seen []string, depth int) string {
	if depth >= maxExpandDepth {
		return s
	}
	return varPattern.ReplaceAllStringFunc(s, func(match string) string {
		token := strings.TrimSpace(varPattern.FindStringSubmatch(match)[1])
		if resolve != nil {
			if val, ok := resolve(token); ok {
				return val // terminal: response data is not rescanned
			}
		}
		// Dynamic variables ({{$uuid}}, {{$randomInt 0 9}}) resolve before the
		// user map, splitting the token into a name and space-separated args.
		if strings.HasPrefix(token, "$") {
			fields := strings.Fields(token)
			if val, ok := dynamic(fields[0], fields[1:]); ok {
				return val // terminal: dynamic value evaluated exactly once
			}
			return match
		}
		if val, ok := v[token]; ok {
			// A token already on the current chain is a cycle (a = {{b}},
			// b = {{a}}); leave it literal rather than recursing forever.
			for _, s := range seen {
				if s == token {
					return match
				}
			}
			return v.expand(val, resolve, append(seen, token), depth+1)
		}
		return match
	})
}
