package httpfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// loadEnvFile reads and parses the http-client.env.json sitting next to the
// plan into a per-environment map of raw JSON values. Values are left raw
// (rather than decoded to strings) so a nested object — notably the IntelliJ
// `Security` block carrying OAuth2 configurations — doesn't break decoding the
// way a flat `map[string]string` would. A missing file yields a nil map (not an
// error) so a plan without environments simply has none.
func loadEnvFile(planPath string) (map[string]map[string]json.RawMessage, error) {
	envPath := filepath.Join(filepath.Dir(planPath), "http-client.env.json")
	data, err := os.ReadFile(envPath)
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

// LoadEnv reads an IntelliJ-style http-client.env.json sitting next to the plan
// and returns the string variables for the named environment. Non-string values
// (such as the `Security` OAuth2 block, consumed by LoadAuth) are skipped. A
// missing file or empty env name yields an empty (but usable) set rather than an
// error.
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
		var s string
		if json.Unmarshal(raw, &s) == nil {
			v[k] = s
		}
	}
	return v, nil
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
			Type              string `json:"Type"`
			GrantType         string `json:"Grant Type"`
			TokenURL          string `json:"Token URL"`
			AuthURL           string `json:"Auth URL"`
			RedirectURL       string `json:"Redirect URL"`
			ClientID          string `json:"Client ID"`
			ClientSecret      string `json:"Client Secret"`
			Scope             string `json:"Scope"`
			Username          string `json:"Username"`
			Password          string `json:"Password"`
			ClientCredentials string `json:"Client Credentials"`
			UseIDToken        bool   `json:"Use ID Token"`
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
		}
	}
	return out, nil
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
