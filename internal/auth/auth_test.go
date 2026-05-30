package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
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
	r := NewResolver(map[string]Config{"api": {GrantType: "Implicit", TokenURL: "http://x"}}, NewCache())
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

// memStore is an in-memory RefreshStore for the Authorization Code tests.
type memStore struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memStore) Get(k string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[k]
}

func (s *memStore) Put(k, v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = v
}

func TestAuthorizationCodeFlow(t *testing.T) {
	ts := &tokenServer{respBody: `{"access_token":"at","refresh_token":"rt","expires_in":3600}`}
	srv := httptest.NewServer(http.HandlerFunc(ts.handler))
	defer srv.Close()

	cfg := Config{
		GrantType:   "Authorization Code",
		AuthURL:     "https://idp.example/authorize",
		TokenURL:    srv.URL,
		RedirectURL: "", // ephemeral loopback listener
		ClientID:    "cid",
		Scope:       "openid profile",
		PKCE:        true,
	}

	// Stub the browser: parse the authorization URL, then hit the redirect the
	// way a provider would after the user signs in.
	var sawChallenge, sawMethod, sawRedirect string
	cache := NewCache().SetInteractive(true).SetStore(&memStore{m: map[string]string{}})
	store := cache.store.(*memStore)
	cache.openURL = func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		q := u.Query()
		sawChallenge = q.Get("code_challenge")
		sawMethod = q.Get("code_challenge_method")
		sawRedirect = q.Get("redirect_uri")
		resp, err := http.Get(q.Get("redirect_uri") + "?code=the-code&state=" + url.QueryEscape(q.Get("state")))
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}

	r := NewResolver(map[string]Config{"idp": cfg}, cache)
	s := &step.Step{Headers: map[string]string{"Authorization": `Bearer {{$auth.token("idp")}}`}}
	if err := r.Resolve(s); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := s.Headers["Authorization"]; got != "Bearer at" {
		t.Errorf("Authorization header: want %q, got %q", "Bearer at", got)
	}

	// PKCE challenge was advertised on the auth URL...
	if sawChallenge == "" || sawMethod != "S256" {
		t.Errorf("expected an S256 PKCE challenge, got challenge=%q method=%q", sawChallenge, sawMethod)
	}
	// ...and the matching verifier + redirect were sent to the token endpoint.
	if ts.lastForm.Get("grant_type") != "authorization_code" {
		t.Errorf("grant_type: %q", ts.lastForm.Get("grant_type"))
	}
	if ts.lastForm.Get("code") != "the-code" {
		t.Errorf("code: %q", ts.lastForm.Get("code"))
	}
	if ts.lastForm.Get("code_verifier") == "" {
		t.Error("expected a PKCE code_verifier in the token exchange")
	}
	if ts.lastForm.Get("redirect_uri") != sawRedirect {
		t.Errorf("redirect_uri mismatch: auth=%q token=%q", sawRedirect, ts.lastForm.Get("redirect_uri"))
	}
	// The refresh token was persisted for next time.
	if store.Get(cacheKey("idp", cfg)) != "rt" {
		t.Errorf("expected the refresh token to be persisted, store=%v", store.m)
	}

	// A second resolve reuses the cached access token — no browser, no fetch.
	cache.openURL = func(string) error { t.Fatal("second resolve should not open a browser"); return nil }
	s2 := &step.Step{Headers: map[string]string{"Authorization": `Bearer {{$auth.token("idp")}}`}}
	if err := r.Resolve(s2); err != nil {
		t.Fatalf("resolve 2: %v", err)
	}
	if ts.hits != 1 {
		t.Errorf("expected a single token exchange, got %d", ts.hits)
	}
}

func TestRefreshTokenRenewsSilently(t *testing.T) {
	ts := &tokenServer{respBody: `{"access_token":"fresh","expires_in":3600}`}
	srv := httptest.NewServer(http.HandlerFunc(ts.handler))
	defer srv.Close()

	cfg := Config{
		GrantType: "Authorization Code",
		AuthURL:   "https://idp.example/authorize",
		TokenURL:  srv.URL,
		ClientID:  "cid",
		PKCE:      true,
	}
	store := &memStore{m: map[string]string{cacheKey("idp", cfg): "saved-rt"}}
	cache := NewCache().SetInteractive(true).SetStore(store)
	cache.openURL = func(string) error { t.Fatal("a saved refresh token should renew without a browser"); return nil }

	r := NewResolver(map[string]Config{"idp": cfg}, cache)
	s := &step.Step{Headers: map[string]string{"Authorization": `Bearer {{$auth.token("idp")}}`}}
	if err := r.Resolve(s); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if s.Headers["Authorization"] != "Bearer fresh" {
		t.Errorf("expected a refreshed token, got %q", s.Headers["Authorization"])
	}
	if ts.lastForm.Get("grant_type") != "refresh_token" || ts.lastForm.Get("refresh_token") != "saved-rt" {
		t.Errorf("expected a refresh_token grant with the saved token, got %v", ts.lastForm)
	}
}

func TestAuthCodeHeadlessNeedsLogin(t *testing.T) {
	// Non-interactive cache (the headless default) with no saved token: the step
	// must fail with a clear, actionable error rather than blocking on a browser.
	cfg := Config{GrantType: "Authorization Code", AuthURL: "https://idp/a", TokenURL: "https://idp/t", ClientID: "cid"}
	r := NewResolver(map[string]Config{"idp": cfg}, NewCache())
	s := &step.Step{Headers: map[string]string{"A": `{{$auth.token("idp")}}`}}
	err := r.Resolve(s)
	if err == nil || !strings.Contains(err.Error(), "browser sign-in") {
		t.Errorf("expected an interactive-sign-in error, got %v", err)
	}
}

func TestAuthCodeEmptyClientIDFailsFast(t *testing.T) {
	// An empty Client ID (e.g. an unset {{$processEnv}}) must fail with a clear
	// error before any browser opens, not send the user to a broken auth page.
	cfg := Config{GrantType: "Authorization Code", AuthURL: "https://idp/a", TokenURL: "https://idp/t", ClientID: ""}
	cache := NewCache().SetInteractive(true)
	cache.openURL = func(string) error { t.Fatal("must not open a browser with an empty Client ID"); return nil }
	r := NewResolver(map[string]Config{"idp": cfg}, cache)
	s := &step.Step{Headers: map[string]string{"A": `{{$auth.token("idp")}}`}}
	err := r.Resolve(s)
	if err == nil || !strings.Contains(err.Error(), "Client ID") {
		t.Errorf("expected a clear empty-Client-ID error, got %v", err)
	}
}

func TestNeedsInteractiveLogin(t *testing.T) {
	cfg := Config{GrantType: "Authorization Code", AuthURL: "https://idp/a", TokenURL: "https://idp/t", ClientID: "c"}
	s := step.Step{Headers: map[string]string{"Authorization": `Bearer {{$auth.token("idp")}}`}}

	// Fresh Authorization Code config with no cached or saved token → browser.
	if r := NewResolver(map[string]Config{"idp": cfg}, NewCache()); !r.NeedsInteractiveLogin(s) {
		t.Error("a fresh Authorization Code config should need an interactive login")
	}
	// A saved refresh token renews silently → no browser.
	store := &memStore{m: map[string]string{cacheKey("idp", cfg): "rt"}}
	if r := NewResolver(map[string]Config{"idp": cfg}, NewCache().SetStore(store)); r.NeedsInteractiveLogin(s) {
		t.Error("a saved refresh token should not require a browser")
	}
	// A back-channel grant never needs a browser.
	cc := Config{GrantType: "Client Credentials", TokenURL: "https://idp/t"}
	if r := NewResolver(map[string]Config{"idp": cc}, NewCache()); r.NeedsInteractiveLogin(s) {
		t.Error("Client Credentials should not require a browser")
	}
}

func TestBuildAuthURLPreservesExistingParams(t *testing.T) {
	// Provider-specific params on the Auth URL (e.g. Google's
	// access_type=offline & prompt=consent, needed to get a refresh token) must
	// survive alongside the standard OAuth2 params lazyhttp adds.
	cfg := Config{
		AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth?access_type=offline&prompt=consent",
		ClientID: "cid",
		Scope:    "openid email",
		PKCE:     true,
	}
	got, err := buildAuthURL(cfg, "http://localhost:8080/callback", "st", "verifier")
	if err != nil {
		t.Fatalf("buildAuthURL: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"access_type":           "offline",
		"prompt":                "consent",
		"response_type":         "code",
		"client_id":             "cid",
		"redirect_uri":          "http://localhost:8080/callback",
		"scope":                 "openid email",
		"state":                 "st",
		"code_challenge_method": "S256",
	} {
		if q.Get(k) != want {
			t.Errorf("param %q = %q, want %q", k, q.Get(k), want)
		}
	}
	if q.Get("code_challenge") == "" {
		t.Error("expected a PKCE code_challenge")
	}
}

func TestConcurrentFetchSingleFlight(t *testing.T) {
	// A token endpoint that blocks until released, so several concurrent callers
	// for the same key pile up on a single in-flight fetch rather than each
	// firing their own (which, for the browser grant, would open N windows).
	release := make(chan struct{})
	hit := make(chan struct{}, 1)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		select {
		case hit <- struct{}{}:
		default:
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
	}))
	defer srv.Close()

	cache := NewCache()
	r := NewResolver(map[string]Config{"api": {GrantType: "Client Credentials", TokenURL: srv.URL, ClientID: "id"}}, cache)

	const n = 6
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := &step.Step{Headers: map[string]string{"A": `{{$auth.token("api")}}`}}
			errs[i] = r.Resolve(s)
		}(i)
	}

	// Once the endpoint is hit, the owning goroutine holds the in-flight slot;
	// give the others a moment to coalesce onto it, then let the fetch finish.
	<-hit
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("resolve %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("expected a single token fetch for %d concurrent callers, got %d", n, got)
	}
}

func TestSlowFetchDoesNotBlockOtherKeys(t *testing.T) {
	// A slow sign-in for one configuration must not hold the cache lock and
	// block resolving a different configuration.
	release := make(chan struct{})
	slowHit := make(chan struct{}, 1)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slowHit <- struct{}{}:
		default:
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"slow","expires_in":3600}`))
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"fast","expires_in":3600}`))
	}))
	defer fast.Close()

	cache := NewCache()
	r := NewResolver(map[string]Config{
		"slow": {GrantType: "Client Credentials", TokenURL: slow.URL, ClientID: "a"},
		"fast": {GrantType: "Client Credentials", TokenURL: fast.URL, ClientID: "b"},
	}, cache)

	go func() {
		s := &step.Step{Headers: map[string]string{"A": `{{$auth.token("slow")}}`}}
		_ = r.Resolve(s)
	}()
	<-slowHit // the slow fetch is now in progress

	done := make(chan error, 1)
	go func() {
		s := &step.Step{Headers: map[string]string{"A": `{{$auth.token("fast")}}`}}
		done <- r.Resolve(s)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("fast resolve: %v", err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("a fast key blocked behind a slow fetch — the cache lock is held across the fetch")
	}
	close(release)
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
