package httpfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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

// LoadEnv reads an IntelliJ-style http-client.env.json sitting next to the plan
// and returns the variables for the named environment. A missing file or empty
// env name yields an empty (but usable) set rather than an error.
func LoadEnv(planPath, envName string) (Vars, error) {
	v := Vars{}
	if envName == "" {
		return v, nil
	}

	envPath := filepath.Join(filepath.Dir(planPath), "http-client.env.json")
	data, err := os.ReadFile(envPath)
	if os.IsNotExist(err) {
		return v, nil
	}
	if err != nil {
		return nil, err
	}

	var envs map[string]map[string]string
	if err := json.Unmarshal(data, &envs); err != nil {
		return nil, err
	}
	for k, val := range envs[envName] {
		v[k] = val
	}
	return v, nil
}

// LoadEnvNames returns the environment names declared in the http-client.env.json
// sitting next to the plan, sorted alphabetically. A missing file yields an empty
// slice (not an error) so a plan without environments simply has none to pick.
func LoadEnvNames(planPath string) ([]string, error) {
	envPath := filepath.Join(filepath.Dir(planPath), "http-client.env.json")
	data, err := os.ReadFile(envPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var envs map[string]map[string]string
	if err := json.Unmarshal(data, &envs); err != nil {
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

// ExpandFunc is Expand with an extra resolver consulted first for each
// placeholder. It lets callers that hold context the variable map can't —
// notably the UI, which resolves inline response references against stored
// results — plug that in without duplicating the matcher. resolve receives the
// trimmed token and returns ok=true to claim it; a nil resolver, or one that
// declines, falls back to dynamic variables and then the variable map, leaving
// unknown placeholders untouched.
func (v Vars) ExpandFunc(s string, resolve func(token string) (string, bool)) string {
	return varPattern.ReplaceAllStringFunc(s, func(match string) string {
		token := strings.TrimSpace(varPattern.FindStringSubmatch(match)[1])
		if resolve != nil {
			if val, ok := resolve(token); ok {
				return val
			}
		}
		// Dynamic variables ({{$uuid}}, {{$randomInt 0 9}}) resolve before the
		// user map, splitting the token into a name and space-separated args.
		if strings.HasPrefix(token, "$") {
			fields := strings.Fields(token)
			if val, ok := dynamic(fields[0], fields[1:]); ok {
				return val
			}
			return match
		}
		if val, ok := v[token]; ok {
			return val
		}
		return match
	})
}
