#!/usr/bin/env python3
"""A throwaway OAuth2 server for exercising lazyhttp's auth helper locally.

It serves on http://127.0.0.1:9000:

  GET  /authorize  a stand-in authorization page for the Authorization Code
                   grant: it shows a one-click "approve" page and redirects back
                   to lazyhttp's localhost callback with a demo `code` — no real
                   account, so the browser round-trip works out of the box.
  POST /token      the token endpoint. Returns a fixed access token for every
                   grant (client_credentials / password / authorization_code /
                   refresh_token), logging each issue so you can see a token is
                   fetched once and reused. The interactive grants also get a
                   refresh_token, so a second run renews silently.
  GET  /...        a "protected" resource that echoes back the Authorization
                   header it received, so you can confirm the bearer token was
                   attached.

Pair it with the `local` environment in http-client.env.json:

    python3 scripts/oauth-demo-server.py        # this script (or: just demo-server)
    lazyhttp --env local example.oauth.http     # client credentials (or: just demo)
    lazyhttp --env local example.browser.http   # browser sign-in (or: just demo-browser)
"""

import html
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, quote, urlparse

HOST, PORT = "127.0.0.1", 9000
issued = 0


class Handler(BaseHTTPRequestHandler):
    def _json(self, status, payload):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _html(self, status, body):
        data = body.encode()
        self.send_response(status)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        global issued
        if urlparse(self.path).path.rstrip("/") != "/token":
            self._json(404, {"error": "not_found"})
            return
        length = int(self.headers.get("Content-Length", 0) or 0)
        form = parse_qs(self.rfile.read(length).decode()) if length else {}
        grant = form.get("grant_type", ["client_credentials"])[0]
        issued += 1
        print(f"  → issued token #{issued} (grant_type={grant})")
        resp = {
            "access_token": f"demo-access-token-{issued}",
            "id_token": "demo-id-token",
            "token_type": "Bearer",
            "expires_in": 3600,
        }
        # Hand back a refresh token for the interactive grants so lazyhttp can
        # renew silently (and a second run skips the browser).
        if grant in ("authorization_code", "refresh_token"):
            resp["refresh_token"] = "demo-refresh-token"
        self._json(200, resp)

    def do_GET(self):
        parsed = urlparse(self.path)
        if parsed.path.rstrip("/") == "/authorize":
            self._authorize(parse_qs(parsed.query))
            return
        auth = self.headers.get("Authorization", "")
        print(f"  → GET {self.path}  (Authorization: {auth or '<none>'})")
        self._json(200, {"path": self.path, "saw_authorization": auth})

    def _authorize(self, q):
        redirect_uri = q.get("redirect_uri", [""])[0]
        state = q.get("state", [""])[0]
        if not redirect_uri:
            self._html(400, "<p>missing redirect_uri</p>")
            return
        sep = "&" if "?" in redirect_uri else "?"
        cont = f"{redirect_uri}{sep}code=demo-auth-code&state={quote(state)}"
        print(f"  → /authorize  (redirect_uri={redirect_uri})")
        self._html(200, f"""<!doctype html>
<html><head><meta charset="utf-8"><title>lazyhttp demo sign-in</title></head>
<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;text-align:center">
  <h2>lazyhttp demo identity provider</h2>
  <p>This is a local stand-in for a real OAuth2 login. Click to approve and
     return to lazyhttp.</p>
  <p><a href="{html.escape(cont)}"
        style="display:inline-block;padding:.6rem 1.2rem;background:#2563eb;color:#fff;border-radius:.4rem;text-decoration:none">
     Approve and continue</a></p>
</body></html>""")

    def log_message(self, *args):  # silence the default access log; we print our own
        pass


if __name__ == "__main__":
    print(f"OAuth2 demo server on http://{HOST}:{PORT}")
    print(f"  authorize : GET  http://{HOST}:{PORT}/authorize  (Authorization Code grant)")
    print(f"  token     : POST http://{HOST}:{PORT}/token")
    print(f"  resource  : GET  http://{HOST}:{PORT}/<anything>")
    print("Run `just demo` or `just demo-browser` in another terminal. Ctrl-C to stop.")
    try:
        ThreadingHTTPServer((HOST, PORT), Handler).serve_forever()
    except KeyboardInterrupt:
        print("\nstopped")
