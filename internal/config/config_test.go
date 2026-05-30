package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTokenStoreRoundTrip persists a refresh token, reads it back through a
// fresh store (forcing a reload from disk), and checks the file landed at mode
// 0600 with no stray temp file left by the atomic write.
func TestTokenStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	s := NewTokenStore()
	if got := s.Get("key"); got != "" {
		t.Fatalf("fresh store Get = %q, want empty", got)
	}
	s.Put("key", "refresh-1")

	// A second store reads the persisted value back from disk.
	if got := NewTokenStore().Get("key"); got != "refresh-1" {
		t.Fatalf("reloaded Get = %q, want %q", got, "refresh-1")
	}

	p, err := tokensPath()
	if err != nil {
		t.Fatalf("tokensPath: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat tokens.json: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("tokens.json mode = %o, want 600", perm)
	}

	// The atomic write must not leave its temp file behind.
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "tokens.json" {
			t.Errorf("unexpected leftover file %q in config dir", e.Name())
		}
	}
}

// TestRoundTrip saves a config and reads it back, with the user-config dir
// redirected into a temp directory so the test never touches the real one.
func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)            // macOS: ~/Library/Application Support
	t.Setenv("XDG_CONFIG_HOME", dir) // Linux: $XDG_CONFIG_HOME

	if got := Load(); got.Theme != "" {
		t.Fatalf("fresh Load() = %+v, want zero Config", got)
	}

	if err := (Config{Theme: "Dracula"}).Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	if got := Load(); got.Theme != "Dracula" {
		t.Fatalf("Load() after Save() = %q, want %q", got.Theme, "Dracula")
	}
}
