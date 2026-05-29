package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

// tokenServer is a stub OAuth2 token endpoint that counts how many times it was
// hit and echoes back the form it received, so tests can assert on caching and
// on the exact grant parameters sent.
type tokenServer struct {
	hits     int
	lastForm url.Values
	lastAuth string
	respBody string
}

func (ts *tokenServer) handler(w http.ResponseWriter, r *http.Request) {
	ts.hits++
	_ = r.ParseForm()
	ts.lastForm = r.PostForm
	ts.lastAuth = r.Header.Get("Authorization")
	w.Header().Set("Content-Type", "application/json")
	if ts.respBody != "" {
		w.Write([]byte(ts.respBody))
		return
	}
	w.Write([]byte(`{"access_token":"tok-` + ts.itoa() + `","id_token":"idt","expires_in":3600,"token_type":"Bearer"}`))
}

func (ts *tokenServer) itoa() string {
	// distinct token per fetch so a test can tell a re-fetch from a cache hit
	return string(rune('0' + ts.hits))
}

// newCacheAt returns a cache whose clock is fixed at t, for deterministic
// expiry tests.
func newCacheAt(t time.Time) *Cache {
	c := NewCache()
	c.now = func() time.Time { return t }
	return c
}

func TestClientCredentialsFetchAndCache(t *testing.T) {
	ts := &tokenServer{}
	srv := httptest.NewServer(http.HandlerFunc(ts.handler))
	defer srv.Close()

	cfg := Config{
		Type:         "OAuth2",
		GrantType:    "Client Credentials",
		TokenURL:     srv.URL,
		ClientID:     "id",
		ClientSecret: "secret",
		Scope:        "read write",
	}
	r := NewResolver(map[string]Config{"api": cfg}, NewCache())

	s := &step.Step{
		URL:     srv.URL + "/data",
		Headers: map[string]string{"Authorization": `Bearer {{$auth.token("api")}}`},
	}
	if err := r.Resolve(s); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := s.Headers["Authorization"]; got != "Bearer tok-1" {
		t.Errorf("header: want %q, got %q", "Bearer tok-1", got)
	}

	// A second step reuses the cached token — no second fetch.
	s2 := &step.Step{Headers: map[string]string{"Authorization": `Bearer {{$auth.token("api")}}`}}
	if err := r.Resolve(s2); err != nil {
		t.Fatalf("resolve 2: %v", err)
	}
	if got := s2.Headers["Authorization"]; got != "Bearer tok-1" {
		t.Errorf("second header should reuse cached token, got %q", got)
	}
	if ts.hits != 1 {
		t.Errorf("expected 1 token fetch, got %d", ts.hits)
	}

	// Client creds default to HTTP Basic, and grant params are correct.
	if ts.lastForm.Get("grant_type") != "client_credentials" {
		t.Errorf("grant_type: %q", ts.lastForm.Get("grant_type"))
	}
	if ts.lastForm.Get("scope") != "read write" {
		t.Errorf("scope: %q", ts.lastForm.Get("scope"))
	}
	if ts.lastAuth == "" || !strings.HasPrefix(ts.lastAuth, "Basic ") {
		t.Errorf("expected Basic client auth, got %q", ts.lastAuth)
	}
	if ts.lastForm.Get("client_secret") != "" {
		t.Error("client_secret should not be in the body when using Basic auth")
	}
}

func TestClientCredentialsInBody(t *testing.T) {
	ts := &tokenServer{}
	srv := httptest.NewServer(http.HandlerFunc(ts.handler))
	defer srv.Close()

	cfg := Config{
		GrantType:         "Client Credentials",
		TokenURL:          srv.URL,
		ClientID:          "id",
		ClientSecret:      "secret",
		ClientCredentials: "in body",
	}
	r := NewResolver(map[string]Config{"api": cfg}, NewCache())
	s := &step.Step{Headers: map[string]string{"Authorization": `Bearer {{$auth.token("api")}}`}}
	if err := r.Resolve(s); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if ts.lastAuth != "" {
		t.Errorf("expected no Basic header for in-body creds, got %q", ts.lastAuth)
	}
	if ts.lastForm.Get("client_id") != "id" || ts.lastForm.Get("client_secret") != "secret" {
		t.Errorf("expected creds in body, got id=%q secret=%q", ts.lastForm.Get("client_id"), ts.lastForm.Get("client_secret"))
	}
}

func TestPasswordGrant(t *testing.T) {
	ts := &tokenServer{}
	srv := httptest.NewServer(http.HandlerFunc(ts.handler))
	defer srv.Close()

	cfg := Config{
		GrantType: "Password",
		TokenURL:  srv.URL,
		ClientID:  "id",
		Username:  "emilys",
		Password:  "emilyspass",
	}
	r := NewResolver(map[string]Config{"api": cfg}, NewCache())
	s := &step.Step{Headers: map[string]string{"Authorization": `Bearer {{$auth.token("api")}}`}}
	if err := r.Resolve(s); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if ts.lastForm.Get("grant_type") != "password" {
		t.Errorf("grant_type: %q", ts.lastForm.Get("grant_type"))
	}
	if ts.lastForm.Get("username") != "emilys" || ts.lastForm.Get("password") != "emilyspass" {
		t.Errorf("username/password not sent: %v", ts.lastForm)
	}
	// No secret + no explicit mode → client_id goes in the body.
	if ts.lastForm.Get("client_id") != "id" {
		t.Errorf("client_id should be in body, got %q", ts.lastForm.Get("client_id"))
	}
}

func TestExpiryRefetch(t *testing.T) {
	ts := &tokenServer{respBody: `{"access_token":"short","expires_in":40}`}
	srv := httptest.NewServer(http.HandlerFunc(ts.handler))
	defer srv.Close()

	base := time.Unix(1_700_000_000, 0)
	cache := newCacheAt(base)
	cfg := Config{GrantType: "Client Credentials", TokenURL: srv.URL, ClientID: "id"}
	r := NewResolver(map[string]Config{"api": cfg}, cache)

	resolve := func() {
		s := &step.Step{Headers: map[string]string{"A": `{{$auth.token("api")}}`}}
		if err := r.Resolve(s); err != nil {
			t.Fatalf("resolve: %v", err)
		}
	}
	resolve()
	if ts.hits != 1 {
		t.Fatalf("first resolve should fetch, hits=%d", ts.hits)
	}
	// expires_in 40 - 30 lead = 10s of validity; at +5s still cached.
	cache.now = func() time.Time { return base.Add(5 * time.Second) }
	resolve()
	if ts.hits != 1 {
		t.Errorf("token still valid at +5s, expected no re-fetch, hits=%d", ts.hits)
	}
	// At +15s it has expired → re-fetch.
	cache.now = func() time.Time { return base.Add(15 * time.Second) }
	resolve()
	if ts.hits != 2 {
		t.Errorf("token expired at +15s, expected re-fetch, hits=%d", ts.hits)
	}
}

func TestBareReferenceUsesSoleConfig(t *testing.T) {
	ts := &tokenServer{}
	srv := httptest.NewServer(http.HandlerFunc(ts.handler))
	defer srv.Close()

	r := NewResolver(map[string]Config{"only": {GrantType: "Client Credentials", TokenURL: srv.URL, ClientID: "id"}}, NewCache())
	s := &step.Step{Headers: map[string]string{"Authorization": "Bearer {{$auth.token}}"}}
	if err := r.Resolve(s); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if s.Headers["Authorization"] != "Bearer tok-1" {
		t.Errorf("bare reference should use the sole config, got %q", s.Headers["Authorization"])
	}
}

func TestBareReferenceAmbiguous(t *testing.T) {
	r := NewResolver(map[string]Config{
		"a": {GrantType: "Client Credentials", TokenURL: "http://x"},
		"b": {GrantType: "Client Credentials", TokenURL: "http://y"},
	}, NewCache())
	s := &step.Step{Headers: map[string]string{"Authorization": "Bearer {{$auth.token}}"}}
	if err := r.Resolve(s); err == nil {
		t.Error("expected an error for an ambiguous bare $auth.token reference")
	}
}

func TestUnknownConfig(t *testing.T) {
	r := NewResolver(map[string]Config{"api": {GrantType: "Client Credentials", TokenURL: "http://x"}}, NewCache())
	s := &step.Step{URL: `http://h/{{$auth.token("nope")}}`}
	if err := r.Resolve(s); err == nil {
		t.Error("expected an error referencing an unknown auth id")
	}
}

func TestUnsupportedGrant(t *testing.T) {
	r := NewResolver(map[string]Config{"api": {GrantType: "Authorization Code", TokenURL: "http://x"}}, NewCache())
	s := &step.Step{Headers: map[string]string{"A": `{{$auth.token("api")}}`}}
	if err := r.Resolve(s); err == nil {
		t.Error("expected an error for an unsupported grant type")
	}
}

func TestTokenEndpointError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid_client","error_description":"bad secret"}`))
	}))
	defer srv.Close()

	r := NewResolver(map[string]Config{"api": {GrantType: "Client Credentials", TokenURL: srv.URL, ClientID: "id"}}, NewCache())
	s := &step.Step{Headers: map[string]string{"A": `{{$auth.token("api")}}`}}
	err := r.Resolve(s)
	if err == nil || !strings.Contains(err.Error(), "bad secret") {
		t.Errorf("expected token-endpoint error surfaced, got %v", err)
	}
}

func TestReferences(t *testing.T) {
	for _, s := range []string{`{{$auth.token("x")}}`, "{{$auth.token}}", `{{ $auth.idToken('y') }}`} {
		if !References(s) {
			t.Errorf("References(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"{{token}}", "plain", "{{$uuid}}"} {
		if References(s) {
			t.Errorf("References(%q) = true, want false", s)
		}
	}
}
