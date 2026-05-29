package httpfile

import (
	"os"
	"path/filepath"
	"testing"
)

const envWithAuth = `{
  "dev": {
    "api": "https://api.example.com",
    "Security": {
      "Auth": {
        "api-cc": {
          "Type": "OAuth2",
          "Grant Type": "Client Credentials",
          "Token URL": "https://id.example.com/token",
          "Client ID": "abc",
          "Client Secret": "{{$processEnv API_SECRET}}",
          "Scope": "read write",
          "Client Credentials": "in body"
        },
        "api-pw": {
          "Type": "OAuth2",
          "Grant Type": "Password",
          "Token URL": "https://id.example.com/token",
          "Username": "{{user}}",
          "Password": "{{pass}}"
        }
      }
    }
  }
}`

// TestLoadEnvSkipsSecurity verifies the Security object doesn't break string-var
// loading and isn't surfaced as a variable: a nested object would once have
// failed JSON decoding into a flat map.
func TestLoadEnvSkipsSecurity(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "http-client.env.json"), []byte(envWithAuth), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := LoadEnv(filepath.Join(dir, "plan.http"), "dev")
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	if v["api"] != "https://api.example.com" {
		t.Errorf("api var: got %q", v["api"])
	}
	if _, ok := v["Security"]; ok {
		t.Error("Security should not appear as a string variable")
	}
}

// TestLoadAuth verifies the Security.Auth block is parsed into Config values
// with the IntelliJ field names mapped and {{var}} placeholders kept intact.
func TestLoadAuth(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "http-client.env.json"), []byte(envWithAuth), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgs, err := LoadAuth(filepath.Join(dir, "plan.http"), "dev")
	if err != nil {
		t.Fatalf("LoadAuth: %v", err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("want 2 configs, got %d", len(cfgs))
	}
	cc := cfgs["api-cc"]
	if cc.GrantType != "Client Credentials" || cc.TokenURL != "https://id.example.com/token" {
		t.Errorf("api-cc fields wrong: %+v", cc)
	}
	if cc.ClientID != "abc" || cc.ClientCredentials != "in body" || cc.Scope != "read write" {
		t.Errorf("api-cc fields wrong: %+v", cc)
	}
	// Placeholders are kept for the caller to expand.
	if cc.ClientSecret != "{{$processEnv API_SECRET}}" {
		t.Errorf("client secret placeholder lost: %q", cc.ClientSecret)
	}
	pw := cfgs["api-pw"]
	if pw.GrantType != "Password" || pw.Username != "{{user}}" || pw.Password != "{{pass}}" {
		t.Errorf("api-pw fields wrong: %+v", pw)
	}
}

// TestLoadAuthNone verifies an env without a Security block (or no env at all)
// yields no configs and no error.
func TestLoadAuthNone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "http-client.env.json"), []byte(`{"dev":{"api":"h"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgs, err := LoadAuth(filepath.Join(dir, "plan.http"), "dev")
	if err != nil {
		t.Fatalf("LoadAuth: %v", err)
	}
	if len(cfgs) != 0 {
		t.Errorf("want no configs, got %d", len(cfgs))
	}
}
