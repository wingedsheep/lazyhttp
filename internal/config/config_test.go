package config

import "testing"

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
