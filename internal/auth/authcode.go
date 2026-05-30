package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// callbackShutdownGrace bounds how long authCodeFlow waits for the local
// redirect server to drain in-flight responses (the success/failure page the
// browser is still receiving) before forcing it down.
const callbackShutdownGrace = 2 * time.Second

// authCodeWait bounds how long the browser flow waits for the redirect before
// giving up, so a step doesn't hang forever if the user never finishes signing
// in (or closes the tab).
const authCodeWait = 3 * time.Minute

// authCodeFlow runs the interactive Authorization Code grant: it starts a
// localhost listener for the redirect, opens the provider's authorization page
// in a browser, waits for the redirect carrying the `code`, and exchanges that
// code (with the PKCE verifier) for tokens at the token endpoint. It is only
// reached when the cache is interactive and no usable token/refresh token
// exists. The returned cachedToken carries the refresh token (if any) so the
// caller can persist it.
func (c *Cache) authCodeFlow(cfg Config) (cachedToken, error) {
	// Validate the required fields up front so a misconfiguration fails with a
	// clear lazyhttp error instead of sending the user to a broken provider page
	// (e.g. an empty Client ID yields Google's opaque "Missing required parameter:
	// client_id"). An empty value here usually means a {{$processEnv VAR}} that
	// wasn't exported in the shell that launched lazyhttp.
	if cfg.AuthURL == "" {
		return cachedToken{}, fmt.Errorf("auth: the Authorization Code grant needs an Auth URL")
	}
	if cfg.TokenURL == "" {
		return cachedToken{}, fmt.Errorf("auth: the Authorization Code grant needs a Token URL")
	}
	if cfg.ClientID == "" {
		return cachedToken{}, fmt.Errorf("auth: Client ID is empty — set it in the Security.Auth config (a {{$processEnv VAR}} resolves to empty when that variable isn't exported in the shell running lazyhttp)")
	}

	// Bind the redirect listener. When a Redirect URL is configured we must bind
	// its exact host:port (it has to match what the provider has registered);
	// otherwise fall back to a loopback address on an OS-chosen port.
	redirectURI, callbackPath, ln, err := listenForRedirect(cfg.RedirectURL)
	if err != nil {
		return cachedToken{}, err
	}
	defer ln.Close()

	// PKCE (on by default) plus a state value to bind the redirect to this flow.
	verifier, err := randomToken(48)
	if err != nil {
		return cachedToken{}, err
	}
	state, err := randomToken(24)
	if err != nil {
		return cachedToken{}, err
	}

	codes := make(chan codeResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			msg := e
			if d := q.Get("error_description"); d != "" {
				msg += ": " + d
			}
			writeCallbackPage(w, false)
			trySend(codes, codeResult{err: fmt.Errorf("auth: authorization denied: %s", msg)})
			return
		}
		if q.Get("state") != state {
			writeCallbackPage(w, false)
			trySend(codes, codeResult{err: fmt.Errorf("auth: redirect state mismatch (possible CSRF) — sign-in aborted")})
			return
		}
		code := q.Get("code")
		if code == "" {
			writeCallbackPage(w, false)
			trySend(codes, codeResult{err: fmt.Errorf("auth: redirect carried no authorization code")})
			return
		}
		writeCallbackPage(w, true)
		trySend(codes, codeResult{code: code})
	})
	// ReadHeaderTimeout bounds a slow client holding the listener open without
	// completing a request line — the redirect callback is a single quick GET.
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	// Shut down gracefully rather than srv.Close(): once the handler has sent the
	// code on the channel and this function returns, an abrupt close can cut the
	// connection before the browser finishes loading the success page, surfacing a
	// connection-reset instead. Shutdown lets the in-flight response drain, bounded
	// by a short grace so a stuck connection can't hang the step. Shutdown also
	// closes the listener, making the later ln.Close() a no-op.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), callbackShutdownGrace)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	// Build and open the authorization URL.
	authURL, err := buildAuthURL(cfg, redirectURI, state, verifier)
	if err != nil {
		return cachedToken{}, err
	}
	if err := c.openURL(authURL); err != nil {
		return cachedToken{}, fmt.Errorf("auth: could not open a browser for sign-in (%w) — visit this URL manually: %s", err, authURL)
	}

	// Wait for the redirect, a timeout, or the listener closing.
	var code string
	select {
	case res := <-codes:
		if res.err != nil {
			return cachedToken{}, res.err
		}
		code = res.code
	case <-time.After(authCodeWait):
		return cachedToken{}, fmt.Errorf("auth: timed out after %s waiting for the browser sign-in to complete", authCodeWait)
	}

	// Exchange the code for tokens.
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	if usePKCE(cfg) {
		form.Set("code_verifier", verifier)
	}
	return c.postToken(cfg, form)
}

// codeResult carries the authorization code (or an error) from the callback
// handler back to authCodeFlow.
type codeResult struct {
	code string
	err  error
}

// trySend delivers r on the buffered channel without blocking if the flow has
// already moved on (e.g. a duplicate redirect after the first).
func trySend(ch chan codeResult, r codeResult) {
	select {
	case ch <- r:
	default:
	}
}

// listenForRedirect opens the loopback listener that catches the OAuth2
// redirect. With a configured Redirect URL it binds that exact host:port and
// serves its path; with none it picks a free loopback port at /callback. It
// returns the redirect_uri to advertise, the path to register a handler on, and
// the listener.
func listenForRedirect(redirectURL string) (uri, path string, ln net.Listener, err error) {
	if redirectURL == "" {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", "", nil, fmt.Errorf("auth: could not start a local redirect listener: %w", err)
		}
		uri = "http://" + ln.Addr().String() + "/callback"
		return uri, "/callback", ln, nil
	}

	u, err := url.Parse(redirectURL)
	if err != nil {
		return "", "", nil, fmt.Errorf("auth: invalid Redirect URL %q: %w", redirectURL, err)
	}
	host := u.Host
	if u.Port() == "" {
		// No explicit port: default to the scheme's port so the bind address is
		// concrete (most providers register an explicit localhost port, though).
		if u.Scheme == "https" {
			host = u.Hostname() + ":443"
		} else {
			host = u.Hostname() + ":80"
		}
	}
	ln, err = net.Listen("tcp", host)
	if err != nil {
		return "", "", nil, fmt.Errorf("auth: could not bind the Redirect URL %q: %w", redirectURL, err)
	}
	path = u.Path
	if path == "" {
		path = "/"
	}
	return redirectURL, path, ln, nil
}

// buildAuthURL assembles the provider's authorization endpoint URL with the
// standard query parameters (and the PKCE challenge when enabled).
func buildAuthURL(cfg Config, redirectURI, state, verifier string) (string, error) {
	u, err := url.Parse(cfg.AuthURL)
	if err != nil {
		return "", fmt.Errorf("auth: invalid Auth URL %q: %w", cfg.AuthURL, err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	if cfg.Scope != "" {
		q.Set("scope", cfg.Scope)
	}
	q.Set("state", state)
	if usePKCE(cfg) {
		sum := sha256.Sum256([]byte(verifier))
		q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
		q.Set("code_challenge_method", "S256")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// usePKCE reports whether the flow should send an S256 code challenge. PKCE is
// on by default for the Authorization Code grant; it is only skipped when a
// config explicitly disables it (see httpfile parsing of the "PKCE" key).
func usePKCE(cfg Config) bool { return cfg.PKCE }

// randomToken returns nbytes of crypto-random data as a URL-safe string,
// suitable for a PKCE verifier (43–128 chars) or a state value.
func randomToken(nbytes int) (string, error) {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: could not generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// writeCallbackPage renders the minimal page the browser lands on after the
// redirect, telling the user the sign-in is done and they can return to the
// terminal.
func writeCallbackPage(w http.ResponseWriter, ok bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	msg := "Sign-in complete — you can close this tab and return to lazyhttp."
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		msg = "Sign-in failed. Check lazyhttp for details; you can close this tab."
	}
	fmt.Fprintf(w, "<!doctype html><html><head><meta charset=\"utf-8\"><title>lazyhttp</title></head><body style=\"font-family:system-ui,sans-serif;padding:3rem;text-align:center\"><h2>lazyhttp</h2><p>%s</p></body></html>", msg)
}
