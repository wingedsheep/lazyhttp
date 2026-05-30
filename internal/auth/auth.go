// Package auth implements the IntelliJ HTTP Client OAuth2 helper: it reads the
// `Security.Auth` blocks of an http-client.env.json, fetches access tokens from
// the configured token endpoint, caches them until they expire, and substitutes
// `{{$auth.token("id")}}` / `{{$auth.idToken("id")}}` placeholders in a step's
// URL, headers and body just before it is sent.
//
// Three grant types are implemented: client_credentials and password (which
// fetch a token with a single back-channel POST) and authorization_code (the
// interactive, browser round-trip grant — see authcode.go). Authorization Code
// uses PKCE by default and persists its refresh token through an injected
// RefreshStore so the browser login happens once and later sessions (and the
// headless `lazyhttp run`) renew silently. The device and implicit grants are
// deliberately left out.
package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

// Config is one OAuth2 authentication configuration, parsed from an entry under
// an environment's `Security.Auth` in http-client.env.json. Field names mirror
// the IntelliJ HTTP Client JSON keys (e.g. "Grant Type", "Client ID").
type Config struct {
	Type              string // expected "OAuth2"
	GrantType         string // "Client Credentials", "Password", or "Authorization Code"
	TokenURL          string
	AuthURL           string // Authorization Code: the browser authorization endpoint
	RedirectURL       string // Authorization Code: the localhost callback to catch the redirect
	ClientID          string
	ClientSecret      string
	Scope             string
	Username          string // Password grant
	Password          string // Password grant
	ClientCredentials string // "basic" (default), "in body", or "none"
	UseIDToken        bool   // attach the id_token rather than the access_token
	PKCE              bool   // Authorization Code: send an S256 code challenge (default on)
}

// authPattern matches the token placeholders this package resolves:
//
//	{{$auth.token}}              {{$auth.token("id")}}     {{$auth.token(id)}}
//	{{$auth.idToken}}            {{$auth.idToken('id')}}
//
// The id argument is optional; when omitted (and exactly one configuration
// exists) that sole configuration is used.
var authPattern = regexp.MustCompile(`\{\{\s*\$auth\.(token|idToken)(?:\s*\(\s*["']?([^"')]*?)["']?\s*\)\s*)?\s*\}\}`)

// RefreshStore persists Authorization Code refresh tokens across sessions,
// keyed by the same string the in-memory cache uses. A nil store means tokens
// live only for the current session. Implementations are best-effort: a failed
// read returns "", a failed write is silently dropped.
type RefreshStore interface {
	Get(key string) string
	Put(key, refresh string)
}

// Cache holds access tokens fetched during a session, keyed by the token
// request's identifying fields, so a plan of many requests performs a single
// token fetch per configuration and reuses it until it expires. It is safe for
// concurrent use: token fetches happen off the UI thread.
//
// For the Authorization Code grant it also carries a RefreshStore (to renew
// silently across sessions), an interactive flag (whether opening a browser is
// permitted — true in the TUI, false for headless runs), and a browser opener.
type Cache struct {
	mu          sync.Mutex
	tokens      map[string]cachedToken
	client      *http.Client
	now         func() time.Time
	store       RefreshStore
	interactive bool
	openURL     func(string) error
}

type cachedToken struct {
	access  string
	id      string
	refresh string
	expiry  time.Time // zero means "no known expiry; reuse for the session"
}

// NewCache returns an empty token cache backed by a 30s-timeout HTTP client. It
// is non-interactive and has no refresh-token store; callers that want the
// Authorization Code browser flow opt in via SetInteractive / SetStore.
func NewCache() *Cache {
	return &Cache{
		tokens:  map[string]cachedToken{},
		client:  &http.Client{Timeout: 30 * time.Second},
		now:     time.Now,
		openURL: openBrowser,
	}
}

// SetStore attaches a refresh-token store so Authorization Code logins persist
// across sessions. Returns the cache for chaining.
func (c *Cache) SetStore(s RefreshStore) *Cache { c.store = s; return c }

// SetInteractive controls whether the Authorization Code grant may open a
// browser. The TUI sets it true; the headless runner leaves it false so a step
// needing a fresh login fails with a clear error instead of blocking on a
// browser that may not exist. Returns the cache for chaining.
func (c *Cache) SetInteractive(v bool) *Cache { c.interactive = v; return c }

// Cached reports whether a live (unexpired) token is already held for key,
// without any network I/O — the TUI uses it to decide whether a step is about
// to trigger an interactive sign-in.
func (c *Cache) Cached(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	tok, ok := c.tokens[key]
	if !ok {
		return false
	}
	return tok.expiry.IsZero() || c.now().Before(tok.expiry)
}

// storedRefresh returns the persisted refresh token for key, or "" when there
// is no store or no saved token.
func (c *Cache) storedRefresh(key string) string {
	if c.store == nil {
		return ""
	}
	return c.store.Get(key)
}

// Resolver substitutes {{$auth.token(...)}} placeholders against a set of
// configurations, fetching (and caching) tokens through a shared Cache. A nil
// Resolver, or one that finds no placeholders, leaves the step untouched.
type Resolver struct {
	configs map[string]Config
	cache   *Cache
}

// NewResolver pairs a set of configurations with a token cache. The
// configurations should already have their {{vars}} expanded by the caller (so
// a client secret sourced from `{{$processEnv …}}` is resolved on the UI thread,
// off the request goroutine).
func NewResolver(configs map[string]Config, cache *Cache) *Resolver {
	return &Resolver{configs: configs, cache: cache}
}

// Resolve replaces every {{$auth.token(...)}} / {{$auth.idToken(...)}}
// placeholder in the step's URL, header values and body with a freshly fetched
// or cached token. It returns the first error encountered (unknown config,
// ambiguous bare reference, or a token-endpoint failure) so the caller can
// surface it as a failed step rather than sending an unauthenticated request.
func (r *Resolver) Resolve(s *step.Step) error {
	if r == nil {
		return nil
	}
	var firstErr error
	sub := func(in string) string {
		return authPattern.ReplaceAllStringFunc(in, func(match string) string {
			m := authPattern.FindStringSubmatch(match)
			kind, id := m[1], m[2]
			val, err := r.lookup(kind, id)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return match // leave the placeholder visible on failure
			}
			return val
		})
	}

	s.URL = sub(s.URL)
	for k, v := range s.Headers {
		s.Headers[k] = sub(v)
	}
	s.Body = sub(s.Body)
	return firstErr
}

// NeedsInteractiveLogin reports whether resolving s would open a browser: it
// references an Authorization Code configuration whose token is neither cached
// in this session nor renewable from a saved refresh token. The TUI calls it on
// the UI thread before dispatching a step to show a "waiting for sign-in"
// notice; it performs no network I/O.
func (r *Resolver) NeedsInteractiveLogin(s step.Step) bool {
	if r == nil {
		return false
	}
	need := false
	scan := func(in string) {
		if need {
			return
		}
		for _, m := range authPattern.FindAllStringSubmatch(in, -1) {
			cfg, key, err := r.configFor(m[2])
			if err != nil || normalizeGrant(cfg.GrantType) != "authorization_code" {
				continue
			}
			if r.cache.Cached(key) || r.cache.storedRefresh(key) != "" {
				continue
			}
			need = true
			return
		}
	}
	scan(s.URL)
	for _, v := range s.Headers {
		scan(v)
	}
	scan(s.Body)
	return need
}

// lookup resolves a single token/idToken reference for the given id (which may
// be empty to mean "the only configuration").
func (r *Resolver) lookup(kind, id string) (string, error) {
	cfg, key, err := r.configFor(id)
	if err != nil {
		return "", err
	}
	tok, err := r.cache.token(key, cfg)
	if err != nil {
		return "", err
	}
	if kind == "idToken" || cfg.UseIDToken {
		if tok.id == "" {
			return "", fmt.Errorf("auth %q: token endpoint returned no id_token", displayID(id))
		}
		return tok.id, nil
	}
	return tok.access, nil
}

// configFor selects the configuration named by id, defaulting to the sole
// configuration when id is empty. The returned key identifies the token request
// for cache purposes.
func (r *Resolver) configFor(id string) (Config, string, error) {
	if id == "" {
		if len(r.configs) != 1 {
			return Config{}, "", fmt.Errorf("$auth.token requires an id: %d auth configurations defined", len(r.configs))
		}
		for k, cfg := range r.configs {
			return cfg, cacheKey(k, cfg), nil
		}
	}
	cfg, ok := r.configs[id]
	if !ok {
		return Config{}, "", fmt.Errorf("auth %q: no such Security.Auth configuration", id)
	}
	return cfg, cacheKey(id, cfg), nil
}

func displayID(id string) string {
	if id == "" {
		return "(default)"
	}
	return id
}

// cacheKey identifies a token request by the fields that determine the token,
// so a config edit (new client, scope, or endpoint) doesn't return a stale
// token even within the same session.
func cacheKey(id string, c Config) string {
	return strings.Join([]string{id, c.GrantType, c.TokenURL, c.ClientID, c.Scope, c.Username}, "\x00")
}

// token returns a cached token for key, fetching a new one when the cache is
// empty or the cached token has expired.
func (c *Cache) token(key string, cfg Config) (cachedToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if tok, ok := c.tokens[key]; ok {
		if tok.expiry.IsZero() || c.now().Before(tok.expiry) {
			return tok, nil
		}
	}
	tok, err := c.obtain(key, cfg)
	if err != nil {
		return cachedToken{}, err
	}
	c.tokens[key] = tok
	return tok, nil
}

// obtain fetches a fresh token for a cache miss. The back-channel grants
// (client_credentials, password) POST once via fetch. Authorization Code first
// tries a persisted refresh token (silent renewal), then — only when
// interactive — opens a browser; headless with no saved token fails with a
// clear, actionable error. A successful Authorization Code login persists its
// refresh token through the store for next time.
func (c *Cache) obtain(key string, cfg Config) (cachedToken, error) {
	if normalizeGrant(cfg.GrantType) != "authorization_code" {
		return c.fetch(cfg)
	}

	if rt := c.storedRefresh(key); rt != "" {
		if tok, err := c.refresh(cfg, rt); err == nil {
			if tok.refresh != "" && tok.refresh != rt {
				c.store.Put(key, tok.refresh) // provider rotated the refresh token
			}
			return tok, nil
		}
		// The saved refresh token was revoked or expired — fall through to a
		// fresh interactive login rather than failing outright.
	}

	if !c.interactive {
		return cachedToken{}, fmt.Errorf("auth: this request needs an interactive browser sign-in (Authorization Code grant) — open the plan in the lazyhttp TUI to sign in once; headless runs then reuse the saved login")
	}

	tok, err := c.authCodeFlow(cfg)
	if err != nil {
		return cachedToken{}, err
	}
	if c.store != nil && tok.refresh != "" {
		c.store.Put(key, tok.refresh)
	}
	return tok, nil
}

// tokenResponse is the subset of an OAuth2 token-endpoint reply we read.
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	IDToken          string `json:"id_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// fetch performs the back-channel token request for cfg (client_credentials or
// password) and parses the response.
func (c *Cache) fetch(cfg Config) (cachedToken, error) {
	form := url.Values{}
	switch normalizeGrant(cfg.GrantType) {
	case "client_credentials":
		form.Set("grant_type", "client_credentials")
	case "password":
		form.Set("grant_type", "password")
		form.Set("username", cfg.Username)
		form.Set("password", cfg.Password)
	default:
		return cachedToken{}, fmt.Errorf("auth: unsupported grant type %q (only Client Credentials, Password, and Authorization Code are supported)", cfg.GrantType)
	}
	if cfg.Scope != "" {
		form.Set("scope", cfg.Scope)
	}
	return c.postToken(cfg, form)
}

// refresh renews an Authorization Code token from a stored refresh token. When
// the endpoint does not return a new refresh token, the existing one is kept so
// it can be reused on the next renewal.
func (c *Cache) refresh(cfg Config, refreshToken string) (cachedToken, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	if cfg.Scope != "" {
		form.Set("scope", cfg.Scope)
	}
	tok, err := c.postToken(cfg, form)
	if err != nil {
		return cachedToken{}, err
	}
	if tok.refresh == "" {
		tok.refresh = refreshToken
	}
	return tok, nil
}

// postToken POSTs an already-built grant form to the token endpoint, applying
// client authentication per cfg (HTTP Basic by default, in the body when
// requested, or omitted) and parsing the JSON response into a cachedToken. It
// is shared by every grant: client_credentials, password, refresh_token, and
// the Authorization Code exchange.
func (c *Cache) postToken(cfg Config, form url.Values) (cachedToken, error) {
	if cfg.TokenURL == "" {
		return cachedToken{}, fmt.Errorf("auth: no Token URL configured")
	}

	// Client authentication: HTTP Basic by default (or when a secret is present
	// and no mode is given), in the body when requested, or omitted entirely.
	mode := strings.ToLower(strings.ReplaceAll(cfg.ClientCredentials, " ", ""))
	useBasic := mode == "basic" || (mode == "" && cfg.ClientSecret != "")
	if !useBasic && mode != "none" {
		if cfg.ClientID != "" && form.Get("client_id") == "" {
			form.Set("client_id", cfg.ClientID)
		}
		if cfg.ClientSecret != "" {
			form.Set("client_secret", cfg.ClientSecret)
		}
	}

	req, err := http.NewRequest(http.MethodPost, cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return cachedToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if useBasic {
		req.SetBasicAuth(cfg.ClientID, cfg.ClientSecret)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return cachedToken{}, fmt.Errorf("auth: token request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return cachedToken{}, err
	}

	var tr tokenResponse
	// Token endpoints reply with JSON; tolerate a missing/garbled body by falling
	// back to the HTTP status for the error message.
	_ = json.Unmarshal(body, &tr)
	if tr.Error != "" {
		msg := tr.Error
		if tr.ErrorDescription != "" {
			msg += ": " + tr.ErrorDescription
		}
		return cachedToken{}, fmt.Errorf("auth: token endpoint rejected request: %s", msg)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cachedToken{}, fmt.Errorf("auth: token endpoint returned %s", resp.Status)
	}
	if tr.AccessToken == "" && tr.IDToken == "" {
		return cachedToken{}, fmt.Errorf("auth: token endpoint returned no access_token")
	}

	tok := cachedToken{access: tr.AccessToken, id: tr.IDToken, refresh: tr.RefreshToken}
	if tr.ExpiresIn > 0 {
		// Refresh a little early so a token isn't used right as it lapses.
		lead := 30
		if tr.ExpiresIn <= lead {
			lead = 0
		}
		tok.expiry = c.now().Add(time.Duration(tr.ExpiresIn-lead) * time.Second)
	}
	return tok, nil
}

// normalizeGrant maps an IntelliJ "Grant Type" value ("Client Credentials",
// "Password") to its OAuth2 grant_type token.
func normalizeGrant(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", "_"))
}

// References reports whether s contains any {{$auth.token(...)}} placeholder, so
// callers can skip building a resolver for steps that need no token.
func References(s string) bool {
	return authPattern.MatchString(s)
}
