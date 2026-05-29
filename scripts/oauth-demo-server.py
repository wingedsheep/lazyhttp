#!/usr/bin/env python3
"""A throwaway OAuth2 server for exercising lazyhttp's auth helper locally.

It serves two things on http://127.0.0.1:9000:

  POST /token   a client-credentials token endpoint that returns a fixed
                access token (logging each issue, so you can see the token is
                fetched once and reused across a plan).
  GET  /...     a "protected" resource that echoes back the Authorization
                header it received, so you can confirm the bearer token was
                attached.

Pair it with the `local` environment in http-client.env.json:

    python3 scripts/oauth-demo-server.py        # this script (or: just demo-server)
    lazyhttp --env local example.oauth.http     # in another terminal (or: just demo)
"""

import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

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

    def do_POST(self):
        global issued
        if self.path.rstrip("/") != "/token":
            self._json(404, {"error": "not_found"})
            return
        issued += 1
        print(f"  → issued token #{issued} (POST {self.path})")
        self._json(200, {
            "access_token": f"demo-access-token-{issued}",
            "id_token": "demo-id-token",
            "token_type": "Bearer",
            "expires_in": 3600,
        })

    def do_GET(self):
        auth = self.headers.get("Authorization", "")
        print(f"  → GET {self.path}  (Authorization: {auth or '<none>'})")
        self._json(200, {"path": self.path, "saw_authorization": auth})

    def log_message(self, *args):  # silence the default access log; we print our own
        pass


if __name__ == "__main__":
    print(f"OAuth2 demo server on http://{HOST}:{PORT}")
    print(f"  token endpoint : POST http://{HOST}:{PORT}/token")
    print(f"  resource       : GET  http://{HOST}:{PORT}/<anything>")
    print("Run `just demo` (or `lazyhttp --env local example.oauth.http`) in another terminal. Ctrl-C to stop.")
    try:
        ThreadingHTTPServer((HOST, PORT), Handler).serve_forever()
    except KeyboardInterrupt:
        print("\nstopped")
