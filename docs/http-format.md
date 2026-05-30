# Writing `.http` files

A plan is a plain text file split into **steps** by `###` separator lines. Each
step is an HTTP request (or a shell command), optionally annotated with `#`
directives that tell lazyhttp how to name it, group it, capture values from the
response, and assert on the result. The base request format is the same one used
by the IntelliJ HTTP Client and the VS Code REST Client — the directives are a
superset, written as `#` comments so the file stays valid in those tools too.

## Step structure

```http
### Optional name on the separator line
# @group Auth
# @name Log in
# @capture token = json.accessToken
# @assert status == 200
POST {{api}}/auth/login
Content-Type: application/json

{ "username": "{{user}}", "password": "{{pass}}" }
```

- **`###`** starts a new step. Any text after it is the step's default name.
- **Directives** (`# @…`) come next, each on its own `#` comment line.
- **Request line** — `METHOD URL`. The method is optional and defaults to `GET`;
  a trailing `HTTP/1.1` is accepted and ignored.
- **Headers** follow, one `Name: value` per line, until a blank line.
- **Body** is everything after that blank line. A body that begins with `< path`
  or `<@ path` loads its contents [from a file](#request-body-from-a-file)
  instead. Response-handler scripts (`> {% … %}`) and pre-request scripts
  (`< {% … %}`) are ignored, not sent.

A step that contains only directives (e.g. a lone `# @group`) produces no
request — but its `@group` still carries forward to the steps below it.

## Directives

All directives are `#` comments. They are case-sensitive and use a **single** `#`
(a `##`-prefixed line is treated as a plain comment, not a directive).

| Directive | Meaning |
| --- | --- |
| `# @name <text>` | Display name for the step (overrides the `###` name). |
| `# @group <text>` | Section heading. Propagates forward to later steps until the next `@group`. |
| `# @capture <var> = <expr>` | Extract a value from the response into `{{var}}` for later steps. |
| `# @assert <expr> [not] <op> [<want>]` | Check a value from the response; the step fails if any assertion fails. |
| `# @shell` | Treat the step body as a shell script instead of an HTTP request. |
| `# @reset` | When this step succeeds, clear every other step's result and drop captured variables — a clean-slate anchor to "run from here". |
| `# @import <path>` | Splice another `.http` file's steps in at this point. See [Composing plans with `@import`](#composing-plans-with-import). |
| `# @timeout <n> <unit>` | Override the shared 30s timeout for this request. See [Per-request settings](#per-request-settings). |
| `# @no-redirect` | Don't follow 3xx redirects; return the redirect response itself. See [Per-request settings](#per-request-settings). |
| `# @stream` | Read the response body incrementally and show it live (SSE, NDJSON, a chunked LLM completion). See [Streaming responses](#streaming-responses). |

You can repeat `# @capture` and `# @assert` as many times as you like in one step.
Plain `#` comments with no recognized directive are simply ignored.

## Expressions (used by `@capture` and the left side of `@assert`)

| Expression | Resolves to |
| --- | --- |
| `status` | HTTP status code (or the shell exit code for `@shell` steps). |
| `body` | The entire response body as a string. |
| `header.<Name>` | A response header value, e.g. `header.Content-Type`. |
| `json.a.b[0].c` | A path into the JSON body. `$.a.b`, `$a.b`, and a bare `a.b` work too. |

JSON paths support nested keys and array indexing (`json.products[0].id`). Values
are stringified naturally: integers have no trailing `.0`, booleans become
`true`/`false`, and objects/arrays are rendered as compact JSON. A path that
doesn't resolve counts as "not found" (fails an `exists`/`==` assertion).

## Assertion operators

| Operator | Passes when |
| --- | --- |
| `# @assert <expr> exists` | the expression resolves to a value. |
| `# @assert <expr> == <want>` | the value equals `<want>` exactly. |
| `# @assert <expr> != <want>` | the value differs from `<want>`. |
| `# @assert <expr> contains <want>` | the value contains `<want>` as a substring. |
| `# @assert <expr> in <a,b,c>` | the value is one of the comma-separated set (whitespace around commas is ignored). |
| `# @assert <expr> > <n>` (also `>=`, `<`, `<=`) | both sides parse as numbers and the comparison holds. |
| `# @assert <expr> matches <regex>` | the value matches the regular expression. |

Prefix any operator with **`not`** to negate it — `not contains`, `not in`,
`not matches`, even `not exists`:

```
# @assert status in 200,204            # a DELETE that may 200 or 204
# @assert json.count > 0               # at least one result
# @assert header.Location matches ^/orders/\d+$
# @assert body not contains error      # negated substring
```

Notes:

- **`in`** compares as strings, so `status in 200,204` is the idiomatic "one of
  several acceptable status codes".
- **Numeric operators** parse *both* sides as numbers; if either side isn't
  numeric the assertion **fails** (it doesn't error) and the result pane shows
  why, e.g. `left is not numeric: "abc"`.
- **`matches`** uses Go's [RE2](https://github.com/google/re2/wiki/Syntax) regex
  syntax. The match is partial unless you anchor it (`^…$`); anchoring is up to
  you. The pattern is taken verbatim — quotes are *not* stripped.

The right-hand `<want>` expands `{{…}}` like anywhere else, so you can assert one
response against a value captured earlier — including a `# @capture` from the
same step, since captures run before assertions:

```
# @capture newId = json.id
# @assert json.id == {{newId}}          # compare against a captured value
```

An unknown variable stays literal (`{{missing}}`), so the comparison fails
visibly rather than matching something unexpected. For `==`, `!=`, `contains`,
and `in`, surrounding single or double quotes are tolerated and stripped, so
`@assert status == "201"` and `@assert status == 201` are equivalent.

## Variables

Use `{{name}}` anywhere in a URL, header, body, or the right-hand side of an
`@assert`. Placeholders are expanded at **execution time**, so a value
`@capture`d from one step flows into the steps below it. An unknown variable is
left as-is (e.g. `{{missing}}`) so you can see what failed to resolve.

Variables can be **composed** from other variables: if a value contains a
`{{…}}` reference, that reference is resolved too, and so on. So `host =
https://api.dev`, `baseUrl = {{host}}/v2`, and a request to `{{baseUrl}}/orders`
reaches `https://api.dev/v2/orders`. This pairs with `{{$processEnv …}}` — define
a host or token once as `baseUrl = {{$processEnv API_BASE}}` and reference
`{{baseUrl}}` everywhere. A self-referential cycle (`a = {{b}}`, `b = {{a}}`) is
left literal rather than looping. Note that captured response data and dynamic
values are inserted as-is and **not** re-expanded, so a response body containing
literal `{{…}}` stays intact.

Values come from three places, later ones overriding earlier:

1. **The environment file** — `http-client.env.json` selected with `--env`.
2. **Inline definitions** — `@name = value` lines anywhere in the file. The value
   may itself reference earlier `{{vars}}`. These are gathered before any step
   runs, so position doesn't matter. Example: `@product = lazyhttp widget`.
3. **Captures** — values pulled from responses by `# @capture`.

### Inline response references

A step can pull a value straight out of an **earlier step's response** without a
`# @capture`, using the VS Code REST Client syntax `{{name.response.…}}`. Give the
source step a name (`# @name login` or a `### login` heading), then reference it:

| Reference | Resolves to |
| --- | --- |
| `{{login.response.body.$.token}}` | A JSON path into the response body (`$.`, `json.`, and a bare path all work). |
| `{{login.response.body.*}}` | The entire response body. |
| `{{login.response.headers.Location}}` | A response header value. |

```http
### login
POST {{api}}/login

###
GET {{api}}/me
Authorization: Bearer {{login.response.body.$.token}}
```

The path after `body.` is evaluated exactly like a `# @capture` expression, so
nested keys and array indexing (`{{login.response.body.items[0].id}}`) work too.
References resolve at **execution time** against the named step's most recent
result; if that step hasn't run yet (or the path doesn't resolve), the
placeholder is left untouched like any unknown variable. `# @capture` remains the
idiomatic lazyhttp approach and reads better in long chains — response references
are here so plans authored for VS Code run unmodified.

### Dynamic variables

`{{$…}}` placeholders are generated fresh each time a step is sent — every
occurrence resolves independently, so two `{{$uuid}}` in one request differ.

| Placeholder | Resolves to |
| --- | --- |
| `{{$uuid}}` / `{{$guid}}` | A random RFC 4122 version-4 UUID. |
| `{{$timestamp}}` | Current time as Unix seconds. |
| `{{$isoTimestamp}}` | Current time as RFC 3339 (UTC). |
| `{{$randomInt [min max]}}` | A random integer in `[min, max)`; defaults to `[0, 1000)`. |
| `{{$datetime <fmt>}}` | Current UTC time formatted as `rfc1123` or `iso8601`. |
| `{{$processEnv <VAR>}}` | The value of environment variable `VAR`. |

An unrecognized dynamic name (e.g. `{{$dotenv}}`, not yet supported) is left
literal, just like an unknown plain variable.

### Environment file

A `http-client.env.json` maps environment names to variable sets; `--env NAME`
picks one:

```json
{
  "dev":     { "api": "https://api.restful-api.dev", "token": "s3cr3t-demo-token" },
  "staging": { "api": "https://api.restful-api.dev", "token": "s3cr3t-staging-token" }
}
```

lazyhttp finds the file by walking up from the plan's directory through its
ancestors and using the first one it sees — so one shared env file at a repo
root (e.g. an `http/` directory) serves plans grouped in subfolders, matching
IntelliJ HTTP Client and VS Code REST Client. The nearest file wins; the search
stops at the repository root (a directory containing `.git`) so it can't escape
the project.

#### Private environment file

A `http-client.private.env.json` in the same directory is layered **over** the
shared file, per variable. Keep secrets there (and out of version control) while
the shared file holds the rest:

```json
// http-client.env.json  (committed)
{ "dev": { "api": "https://api.dev", "token": "{{clientSecret}}" } }

// http-client.private.env.json  (gitignored)
{ "dev": { "clientSecret": "s3cr3t-real-value" } }
```

For environment `dev` the merged set is `api`, `token`, and `clientSecret`, with
any key present in both taken from the private file. This also covers the
`Security.Auth` pattern: declare the auth block (referencing `{{clientSecret}}`)
in the shared file and put `clientSecret` in the private file.

### OAuth2 authentication

For APIs behind OAuth2, lazyhttp can fetch and attach a bearer token for you
instead of you hand-rolling a login request and `@capture`-ing the token. It
honors the IntelliJ HTTP Client's `Security.Auth` block in
`http-client.env.json`:

```json
{
  "dev": {
    "api": "https://api.example.com",
    "Security": {
      "Auth": {
        "demo": {
          "Type": "OAuth2",
          "Grant Type": "Client Credentials",
          "Token URL": "https://id.example.com/oauth/token",
          "Client ID": "demo-client",
          "Client Secret": "{{$processEnv OAUTH_CLIENT_SECRET}}",
          "Scope": "read",
          "Client Credentials": "basic"
        }
      }
    }
  }
}
```

Reference a configuration by id in a request and lazyhttp resolves it to a token:

```http
### Protected request
GET {{api}}/me
Authorization: Bearer {{$auth.token("demo")}}
```

- **`{{$auth.token("id")}}`** resolves to the configuration's access token;
  **`{{$auth.idToken("id")}}`** resolves to its `id_token`. With exactly one
  configuration defined you may drop the id: `{{$auth.token}}`.
- **The token is fetched once and cached** for the rest of the session, honoring
  the endpoint's `expires_in` (refetched a little early, and again once expired),
  so a plan of twenty requests performs a single token fetch.
- **Grant types:** `Client Credentials`, `Password`, and `Authorization Code`
  (the interactive, browser-based grant — see below). `Device Authorization` and
  `Implicit` are **not** supported.
- **Client authentication** follows `Client Credentials`: `"basic"` (HTTP Basic,
  the default when a secret is present), `"in body"` (`client_id`/`client_secret`
  in the form), or `"none"`. `Password` grant additionally reads `Username` and
  `Password`.
- **Secrets stay out of the plan.** Configuration values expand `{{vars}}` and
  dynamic variables, so a secret can come from `{{$processEnv VAR}}` or the env
  file. The request preview (`i`) shows the literal `{{$auth.token(...)}}`
  placeholder, never the resolved token.
- The token fetch happens off the UI thread, so a slow token endpoint never
  freezes the interface.

> Like `@import`, this reuses the IntelliJ JSON shape, so a plan that uses
> `{{$auth.token(...)}}` with a `Security.Auth` env block stays portable to the
> IntelliJ HTTP Client. See [`example.oauth.http`](../example.oauth.http).

#### Authorization Code (browser sign-in)

For user-facing APIs (Google, GitHub, Okta, Auth0, most SaaS) use the
`Authorization Code` grant. lazyhttp opens your browser to the provider's login
page, catches the redirect on a localhost listener, and exchanges the code for a
token — using PKCE (S256) by default.

To see the whole round-trip with **no setup**, run
[`example.browser.http`](../example.browser.http) against the bundled demo
identity provider (`just demo-server`, then `just demo-browser`) — no accounts,
client IDs, or secrets.

For a real provider, point the same config shape at the provider's endpoints.
Here is a complete Google config (create a "Web application" OAuth client, add
`http://localhost:8080/callback` to its authorized redirect URIs, and put the
Client ID inline / the secret in `{{$processEnv GOOGLE_CLIENT_SECRET}}`):

```json
{
  "google": {
    "Security": {
      "Auth": {
        "google": {
          "Type": "OAuth2",
          "Grant Type": "Authorization Code",
          "Auth URL": "https://accounts.google.com/o/oauth2/v2/auth?access_type=offline&prompt=consent",
          "Token URL": "https://oauth2.googleapis.com/token",
          "Redirect URL": "http://localhost:8080/callback",
          "Client ID": "{{$processEnv GOOGLE_CLIENT_ID}}",
          "Client Secret": "{{$processEnv GOOGLE_CLIENT_SECRET}}",
          "Scope": "openid email profile",
          "Client Credentials": "in body"
        }
      }
    }
  }
}
```

- **`Auth URL`** is the browser authorization endpoint; **`Token URL`** is the
  back-channel code-exchange endpoint. **`Redirect URL`** must match a callback
  **registered with the provider** (for Google, add it under your OAuth client's
  *Authorized redirect URIs*) — lazyhttp binds that exact `localhost` port and
  path to catch the redirect. Omit it and lazyhttp picks a free loopback port at
  `/callback` (only works if the provider accepts a dynamic loopback redirect).
- **Provider-specific params** go right on the `Auth URL` query string — lazyhttp
  keeps them and adds the standard OAuth2 params alongside. Google needs
  `access_type=offline&prompt=consent` to return a **refresh token**; without it
  you'd re-sign-in every session.
- **PKCE is on by default.** Add `"PKCE": false` to disable the S256 challenge
  for a provider that rejects it; a public client (no `Client Secret`) relies on
  PKCE alone.
- **`Client Credentials`** controls how the client authenticates at the token
  endpoint, same as the other grants: `"in body"` (Google's documented way),
  `"basic"`, or `"none"`.
- **Sign in once.** After a successful login the **refresh token is saved** to
  `tokens.json` in your OS config dir (mode `0600`), so later sessions renew the
  token silently without reopening the browser.
- **Run from the TUI first.** Only the interactive TUI opens a browser. The
  headless `lazyhttp run` reuses a saved refresh token when one exists; with no
  saved login it fails the step with a clear message rather than blocking — so
  sign in once in the TUI, then CI can run headlessly off the stored token.

### Basic authentication

An `Authorization: Basic` header with raw credentials is base64-encoded for you,
matching the IntelliJ HTTP Client and VS Code REST Client shorthand. `{{vars}}`
are expanded *before* encoding:

```
### Space-separated user and password
GET {{api}}/admin
Authorization: Basic {{amq_user}} {{amq_pass}}
```

Three credential forms are recognized after the (case-insensitive) `Basic`
scheme:

| Header in file | Sent on the wire |
| --- | --- |
| `Basic alice s3cret` | `Basic YWxpY2U6czNjcmV0` |
| `Basic alice:s3cret` | `Basic YWxpY2U6czNjcmV0` |
| `Basic YWxpY2U6czNjcmV0` (already base64) | `Basic YWxpY2U6czNjcmV0` (unchanged) |

A single token without a colon is assumed already base64-encoded and passes
through untouched, so there's no double-encoding. A password containing
whitespace can't be expressed with the space-separated shorthand — use the
`user:password` colon form or pre-encode it.

## Request body from a file

Instead of an inline body, a step can load its request body from a file. The
reference is the **first line** of the body (after the blank line that ends the
headers):

```
### Upload
POST {{api}}/objects
Content-Type: application/json

< ./payload.json
```

- **`< path`** sends the file's contents verbatim — `{{vars}}` inside the file
  are **not** expanded.
- **`<@ path`** expands `{{vars}}` (and dynamic variables) in the file's contents
  before sending, just like an inline body.
- **`<@encoding path`** (e.g. `<@latin1 ./body.txt`) is accepted; the encoding
  token is currently ignored and the file is read as UTF-8.

The path is resolved relative to the **plan file's directory**. If the file can't
be read, the step fails with the read error rather than sending an empty body.
Toggle the request preview (`i`) to see the resolved `body from < path` and its
contents.

## Per-request settings

Two directives override the default request behavior for a single step. Both are
HTTP-only; they're ignored on `# @shell` steps.

```
### Slow report — give it longer, and don't chase redirects
# @timeout 90 s
# @no-redirect
GET {{api}}/reports/export
```

- **`# @timeout <n> <unit>`** replaces the shared 30-second client timeout for
  this request. Units are `ms`, `s`, `m`, and `h`; a bare number (`# @timeout 45`)
  is read as seconds. The combined form works too, including fractional and
  compound durations (`# @timeout 500ms`, `# @timeout 1.5s`, `# @timeout 1h30m`).
  A zero/negative value or an unknown unit is ignored, leaving the 30s default.
- **`# @no-redirect`** stops the client following `3xx` `Location` headers, so the
  redirect response itself is returned — handy for asserting on the redirect
  (`# @assert status == 302`, `# @assert header.Location matches ^/orders/\d+$`).
  For a `@no-redirect` step a `3xx` counts as **success** (it's the outcome you
  asked for), so the step stays green and a "run from here" chain continues;
  without the directive a leaked `3xx` is still treated as a failure.

Both apply identically in the TUI and in headless `lazyhttp run`. When the request
preview (`i`) is open, an active setting shows as a `⚙ timeout 90s · no-redirect`
line under the request.

## Streaming responses

`# @stream` opts a step into incremental delivery: instead of waiting for the
whole response, lazyhttp reads the body as it arrives and appends it to the
response pane live, scrolling as new text comes in. It's built for endpoints that
hold a connection open and emit data over time — Server-Sent Events
(`text/event-stream`), newline-delimited JSON (`application/x-ndjson`), and the
chunked token streams that LLM chat APIs produce.

```
### Watch an LLM completion stream in token by token
# @group AI
# @name OpenRouter chat (streaming)
# @stream
# @timeout 5 m
# @assert status == 200
POST https://openrouter.ai/api/v1/chat/completions
Authorization: Bearer {{$processEnv OPENROUTER_API_KEY}}
Content-Type: application/json

{
  "model": "anthropic/claude-haiku-4.5",
  "stream": true,
  "messages": [
    { "role": "user", "content": "In two sentences, what is a terminal UI?" }
  ]
}
```

Run it and the SSE frames (`data: {…}` lines, ending with `data: [DONE]`) fill in
as the model generates them. Set `OPENROUTER_API_KEY` in your environment first
(`{{$processEnv …}}` reads it at send time); a runnable copy lives in
[`examples/openrouter-stream.http`](../examples/openrouter-stream.http).

### Making a stream legible

The raw SSE wire format is noisy — keepalive comments, the `data:` envelope, and
a whole JSON object per token, when all you want is the text. Two optional
transforms clean it up, both declared in the step:

**Built-in field extraction** — give `# @stream` a JSON path and lazyhttp parses
each SSE `data:` frame, drops the comments and the `[DONE]` sentinel, and emits
just that field. No external tools. The path uses the same JSON-path syntax as
`# @capture` (see [Expressions](#expressions-used-by-capture-and-the-left-side-of-assert)):

```
# @stream choices[0].delta.content
```

So `{"choices":[{"delta":{"content":"hel"}}]}` followed by `{… "content":"lo"}`
streams in as `hello`.

**External command pipe** — `# @stream-through <command>` runs the live stream
through any shell command via stdin → stdout, for formats the built-in extractor
doesn't cover. Here `jq` does the same job:

```
# @stream-through jq -Rj --unbuffered 'ltrimstr("data: ") | select(startswith("{")) | fromjson | .choices[0].delta.content // empty'
```

The command runs in your shell (the same one `# @shell` uses); its stdout is
what you see and what captures/assertions run against. A non-zero exit fails the
step with the command's stderr. If both are set, `# @stream-through` wins.

Notes:

- **Raw by default.** With neither transform, the body isn't reformatted as JSON
  — the event framing would be destroyed — so you see exactly what the server sent.
- **Captures and assertions run on close.** Once the stream ends, `# @capture` and
  `# @assert` evaluate against the full accumulated (and transformed) body, exactly
  as for a normal response. (Per-event assertions aren't a thing yet.)
- **No 30s cap.** A streaming step ignores the shared 30-second client timeout (it
  would cut the stream off); bound a long one with `# @timeout` instead. Press `c`
  on a running stream to stop it.
- **Headless works too.** `lazyhttp run` reads the stream to completion, applies the
  same transform, and asserts on close, so a streaming step is CI-friendly without
  changes.

## Composing plans with `@import`

A shared login flow, a set of common headers, or a block of `@var` definitions
often needs to be reused across several plans. Instead of copy-pasting it,
`# @import` pulls another `.http` file in:

```http
### Sign in first
# @import ./auth.http

### Now use the captured token
GET {{api}}/me
Authorization: Bearer {{token}}
```

An `@import` block contributes the **imported file's steps**, spliced in at the
point of import in their original order — exactly as if you had pasted them in.
That means everything flows naturally:

- **Captures flow forward.** A `# @capture token = json.accessToken` in
  `auth.http` populates `{{token}}` for every step after the import, because
  expansion happens at execution time against one shared variable set.
- **Inline `@var` definitions are merged.** A `@token = …` line in the imported
  file joins the plan's variables like any other inline definition.
- **The imported steps appear in the list** and run with "run from here" / "run
  all" just like local steps; each keeps its own `@group`.

The path is resolved relative to the **importing file's directory**, so a nested
import inside the imported file resolves against *its* own location (the same
rule as a [`< body` file](#request-body-from-a-file)). An `@import` block holds
nothing but the directive — put the import on its own `###` section.

Imports may nest, but a **cycle** (`a.http` → `b.http` → `a.http`) is an error,
as is importing a file that can't be read; either fails the load with a clear
message rather than hanging or silently dropping steps. Importing the same file
from two places is fine — its steps are contributed once per import.

> `@import` is a lazyhttp extension written as a `#` comment, so a plan that uses
> it stays parseable in IntelliJ / VS Code (which simply ignore the directive and
> don't pull the file in).

## Shell steps

A step marked `# @shell` runs its body as a script via the platform's shell —
`$SHELL -c` (falling back to `/bin/sh`) on macOS/Linux, and PowerShell
(`powershell -NoProfile -Command`) on Windows. `{{vars}}` are expanded first,
stdout and stderr are combined into the result body, and `status` in an assertion
refers to the exit code:

```http
### Print captured values
# @shell
# @name Echo state
# @assert status == 0
echo "token = {{token}}"
```

Because the whole block is a `#`-commented shell directive plus a script, IntelliJ
and VS Code treat it as a comment and ignore it — so the plan stays portable.

Shell bodies themselves are **not** portable across operating systems: a body
written for POSIX `sh` (`&&` chaining, `$VAR`, single-quoting) won't all carry
over to PowerShell. See [Windows notes](#windows-notes) if you share plans across
platforms.

See [`example.http`](../example.http) for a complete, runnable tour of every feature.

## Compatibility notes

lazyhttp reads the same `.http` format as the IntelliJ HTTP Client and the VS Code
REST Client, but it implements a focused subset plus its own extensions (`# @group`,
`# @capture`, `# @assert`, `# @shell`, `# @reset`, `# @import`). The features below exist in one
or both of those tools and are **not supported yet**. A plan using them stays
parseable — lazyhttp will simply ignore or pass through the unsupported part — but
it won't behave the way it does in the original tool.

### Not supported yet

Each entry lists the upstream syntax and what lazyhttp does with it today.

- **`{{$dotenv VAR}}`** — the one dynamic variable not yet wired up; it needs
  `.env` file discovery. Left literal for now. (Other dynamic variables —
  `{{$uuid}}`, `{{$timestamp}}`, `{{$randomInt}}`, `{{$datetime}}`,
  `{{$processEnv}}` — are supported; see [Dynamic variables](#dynamic-variables).)
- **Response-handler scripts** — `> {% client.test(...); client.global.set(...) %}`.
  Ignored (lines starting with `>` are dropped). Use `# @capture` / `# @assert`.
- **Pre-request scripts** — `< {% request.variables.set(...) %}`.
  Recognized as a script and ignored (not sent as a body). The variables it would
  set are not applied.
- **Per-request settings** — `# @no-cookie-jar`, `# @no-log`, `# @prompt {pw}`,
  `# @note`. Unrecognized directives are ignored (treated as plain comments).
  (`# @no-redirect` and `# @timeout` *are* supported — see
  [Per-request settings](#per-request-settings).)
- **Multipart / form-data uploads** — `Content-Type: multipart/form-data; boundary=...`
  with `< ./file` parts. The body text is sent as-is; file parts are not read or attached.
- **GraphQL requests** — header `X-REQUEST-TYPE: GraphQL` followed by a query and
  variables. No special handling; sent as a plain body.
- **Auth sugar** — `Digest`, `AWS`, Azure AD.
  Sent verbatim — these schemes need request signing, which lazyhttp doesn't do.
  (`Basic` auth shorthand *is* supported — see
  [Basic authentication](#basic-authentication) — and OAuth2 *is* supported via
  `Security.Auth` + `{{$auth.token(...)}}`; see
  [OAuth2 authentication](#oauth2-authentication) — covering the Client
  Credentials, Password, and Authorization Code grants.)
- **`//` comment/directive prefix** — `// @name Foo`.
  Not recognized; lazyhttp directives require a `#` prefix.
- **Multi-line URLs** — continuation lines starting with `?` / `&`.
  Only the first line is read as the request line.
- **`$shared` / private env files** — `$shared` env, `http-client.private.env.json`.
  Only `http-client.env.json` is read; the named env is used as-is.

If you hit one of these and want it supported, it's worth opening an issue.

## Windows notes

lazyhttp runs on Windows as a first-class target. A few things behave
differently there:

- **`@shell` interpreter is chosen from your environment.** The order is:
  1. `LAZYHTTP_SHELL`, if set — an explicit override (`cmd`, `powershell`,
     `bash`, or a full path).
  2. `$SHELL`, if set — Git Bash, MSYS2, and Cygwin export it (native cmd and
     PowerShell don't), so launching lazyhttp from one of those Unix-like shells
     runs `@shell` bodies through `bash -c` automatically, just as on Linux/macOS.
  3. PowerShell (`powershell -NoProfile -Command <body>`) — the default for a
     native cmd/PowerShell session.

  So a POSIX-style body works as-is from Git Bash, while a native PowerShell
  session gets PowerShell. Bodies are still not portable *across* interpreters,
  so if you share a plan across platforms, keep `@shell` bodies simple (a bare
  `echo` works everywhere) or write them for the shell you'll actually run on.
- **CRLF line endings are tolerated.** `.http` plans authored on Windows (with
  `\r\n` line endings) parse identically to Unix-authored ones — no stray
  carriage returns leak onto URLs, header values, or captured variables.
- **Install.** Download `lazyhttp_windows_<arch>.zip` from the
  [latest release](https://github.com/wingedsheep/lazyhttp/releases/latest),
  unzip it, and put `lazyhttp.exe` on your `PATH`. Or, with
  [Scoop](https://scoop.sh):

  ```powershell
  scoop bucket add wingedsheep https://github.com/wingedsheep/scoop-bucket
  scoop install lazyhttp
  ```

  Use Windows Terminal (or modern PowerShell) for correct colors, key handling,
  and mouse-wheel scrolling.
