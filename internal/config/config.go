// Package config persists user preferences (currently just the colour theme)
// across launches in a small JSON file under the OS user-config directory.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
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

// tokensPath returns the OAuth2 refresh-token store location, a sibling of the
// config file. Refresh tokens are secrets, so they live in their own file
// (written 0600) rather than in config.json.
func tokensPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lazy-http", "tokens.json"), nil
}

// TokenStore is a file-backed implementation of auth.RefreshStore: it persists
// Authorization Code refresh tokens to tokens.json (mode 0600) so a browser
// sign-in survives restarts and the headless runner can renew silently. It is
// best-effort — a read or write failure degrades gracefully to in-memory only —
// and safe for concurrent use, since token fetches happen off the UI thread.
type TokenStore struct {
	mu     sync.Mutex
	tokens map[string]string
	loaded bool
}

// NewTokenStore returns a lazily-loaded token store.
func NewTokenStore() *TokenStore { return &TokenStore{} }

// load reads tokens.json once, on first access. A missing or malformed file
// yields an empty store rather than an error.
func (s *TokenStore) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.tokens = map[string]string{}
	p, err := tokensPath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &s.tokens)
}

// Get returns the saved refresh token for key, or "" if there is none.
func (s *TokenStore) Get(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	return s.tokens[key]
}

// Put saves refresh under key and rewrites tokens.json. A no-op when the value
// is unchanged; the write is best-effort.
func (s *TokenStore) Put(key, refresh string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if s.tokens[key] == refresh {
		return
	}
	s.tokens[key] = refresh
	p, err := tokensPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(s.tokens, "", "  ")
	if err != nil {
		return
	}
	_ = writeFileAtomic(p, data, 0o600)
}

// writeFileAtomic writes data to a temp file in the destination directory and
// renames it into place, so a crash or a concurrent run mid-write can never
// leave a half-written (corrupt) file: a reader sees either the old contents or
// the complete new ones. The temp file is created in the same directory as the
// target so the rename stays on one filesystem (and thus atomic). perm is
// applied before the rename so the secret never exists with looser permissions.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename; a no-op once renamed.
	defer os.Remove(tmpName)

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
