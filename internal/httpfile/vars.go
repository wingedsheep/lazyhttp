package httpfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// varPattern matches IntelliJ-style placeholders such as {{host}} and dynamic
// variables with optional args such as {{$uuid}} or {{$randomInt 0 9}}.
var varPattern = regexp.MustCompile(`\{\{\s*(\$?[\w.-]+(?:\s+[^}]+)?)\s*\}\}`)

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
	return varPattern.ReplaceAllStringFunc(s, func(match string) string {
		token := strings.TrimSpace(varPattern.FindStringSubmatch(match)[1])
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
