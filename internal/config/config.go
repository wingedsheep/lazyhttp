// Package config persists user preferences (currently just the colour theme)
// across launches in a small JSON file under the OS user-config directory.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds the preferences carried between runs. Fields are omitempty so an
// unset preference leaves no key behind and future fields stay backward-compatible.
type Config struct {
	Theme string `json:"theme,omitempty"`
}

// path returns the config file location, e.g. ~/.config/lazy-http/config.json on
// Linux or ~/Library/Application Support/lazy-http/config.json on macOS.
func path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lazy-http", "config.json"), nil
}

// Load reads the saved config. A missing file (or an unreadable user-config
// directory) yields a zero Config and no error, so first runs start clean.
func Load() Config {
	p, err := path()
	if err != nil {
		return Config{}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Config{}
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}
	}
	return c
}

// Save writes the config, creating the directory if needed. It is best-effort:
// callers persisting a preference treat a write failure as non-fatal.
func (c Config) Save() error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}
