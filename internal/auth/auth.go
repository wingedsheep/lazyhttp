// Package auth implements the IntelliJ HTTP Client OAuth2 helper: it reads the
// `Security.Auth` blocks of an http-client.env.json, fetches access tokens from
// the configured token endpoint, caches them until they expire, and substitutes
// `{{$auth.token("id")}}` / `{{$auth.idToken("id")}}` placeholders in a step's
// URL, headers and body just before it is sent.
//
// Only the two grant types that work without a browser round-trip are
// implemented — client_credentials and password — which between them cover the
// vast majority of machine-to-machine and CLI API access. The interactive
// grants (authorization code, device, implicit) are deliberately left out.
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
	GrantType         string // "Client Credentials" or "Password"
	TokenURL          string
	AuthURL           string
	RedirectURL       string
	ClientID          string
	ClientSecret      string
	Scope             string
	Username          string // Password grant
	Password          string // Password grant
	ClientCredentials string // "basic" (default), "in body", or "none"
	UseIDToken        bool   // attach the id_token rather than the access_token
}

// authPattern matches the token placeholders this package resolves:
//
//	{{$auth.token}}              {{$auth.token("id")}}     {{$auth.token(id)}}
//	{{$auth.idToken}}            {{$auth.idToken('id')}}
//
// The id argument is optional; when omitted (and exactly one configuration
// exists) that sole configuration is used.
var authPattern = regexp.MustCompile(`\{\{\s*\$auth\.(token|idToken)(?:\s*\(\s*["']?([^"')]*?)["']?\s*\)\s*)?\s*\}\}`)

// Cache holds access tokens fetched during a session, keyed by the token
// request's identifying fields, so a plan of many requests performs a single
// token fetch per configuration and reuses it until it expires. It is safe for
// concurrent use: token fetches happen off the UI thread.
type Cache struct {
	mu     sync.Mutex
	tokens map[string]cachedToken
	client *http.Client
	now    func() time.Time
}

type cachedToken struct {
	access string
	id     string
	expiry time.Time // zero means "no known expiry; reuse for the session"
}

// NewCache returns an empty token cache backed by a 30s-timeout HTTP client.
func NewCache() *Cache {
	return &Cache{
		tokens: map[string]cachedToken{},
		client: &http.Client{Timeout: 30 * time.Second},
		now:    time.Now,
	}
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
	tok, err := c.fetch(cfg)
	if err != nil {
		return cachedToken{}, err
	}
	c.tokens[key] = tok
	return tok, nil
}

// tokenResponse is the subset of an OAuth2 token-endpoint reply we read.
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	IDToken          string `json:"id_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// fetch performs the token request for cfg and parses the response.
func (c *Cache) fetch(cfg Config) (cachedToken, error) {
	grant := normalizeGrant(cfg.GrantType)
	if cfg.TokenURL == "" {
		return cachedToken{}, fmt.Errorf("auth: no Token URL configured")
	}

	form := url.Values{}
	switch grant {
	case "client_credentials":
		form.Set("grant_type", "client_credentials")
	case "password":
		form.Set("grant_type", "password")
		form.Set("username", cfg.Username)
		form.Set("password", cfg.Password)
	default:
		return cachedToken{}, fmt.Errorf("auth: unsupported grant type %q (only Client Credentials and Password are supported)", cfg.GrantType)
	}
	if cfg.Scope != "" {
		form.Set("scope", cfg.Scope)
	}

	// Client authentication: HTTP Basic by default (or when a secret is present
	// and no mode is given), in the body when requested, or omitted entirely.
	mode := strings.ToLower(strings.ReplaceAll(cfg.ClientCredentials, " ", ""))
	useBasic := mode == "basic" || (mode == "" && cfg.ClientSecret != "")
	if !useBasic && mode != "none" {
		if cfg.ClientID != "" {
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

	tok := cachedToken{access: tr.AccessToken, id: tr.IDToken}
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
