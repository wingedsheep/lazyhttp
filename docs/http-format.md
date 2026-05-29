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
| `# @assert <expr> <op> [<want>]` | Check a value from the response; the step fails if any assertion fails. |
| `# @shell` | Treat the step body as a shell script instead of an HTTP request. |
| `# @reset` | When this step succeeds, clear every other step's result and drop captured variables — a clean-slate anchor to "run from here". |

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

The right-hand `<want>` is compared **literally** — don't wrap it in `{{…}}`.
Surrounding single or double quotes are tolerated and stripped, so
`@assert status == "201"` and `@assert status == 201` are equivalent.

## Variables

Use `{{name}}` anywhere in a URL, header, or body. Placeholders are expanded at
**execution time**, so a value `@capture`d from one step flows into the steps
below it. An unknown variable is left as-is (e.g. `{{missing}}`) so you can see
what failed to resolve.

Values come from three places, later ones overriding earlier:

1. **The environment file** — `http-client.env.json` selected with `--env`.
2. **Inline definitions** — `@name = value` lines anywhere in the file. The value
   may itself reference earlier `{{vars}}`. These are gathered before any step
   runs, so position doesn't matter. Example: `@product = lazyhttp widget`.
3. **Captures** — values pulled from responses by `# @capture`.

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

A `http-client.env.json` sitting next to the plan maps environment names to
variable sets; `--env NAME` picks one:

```json
{
  "dev":     { "api": "https://api.restful-api.dev", "token": "s3cr3t-demo-token" },
  "staging": { "api": "https://api.restful-api.dev", "token": "s3cr3t-staging-token" }
}
```

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

## Shell steps

A step marked `# @shell` runs its body as a script via `$SHELL -c` (falling back
to `/bin/sh`). `{{vars}}` are expanded first, stdout and stderr are combined into
the result body, and `status` in an assertion refers to the exit code:

```http
### Print captured values
# @shell
# @name Echo state
# @assert status == 0
echo "token = {{token}}"
```

Because the whole block is a `#`-commented shell directive plus a script, IntelliJ
and VS Code treat it as a comment and ignore it — so the plan stays portable.

See [`example.http`](../example.http) for a complete, runnable tour of every feature.

## Compatibility notes

lazyhttp reads the same `.http` format as the IntelliJ HTTP Client and the VS Code
REST Client, but it implements a focused subset plus its own extensions (`# @group`,
`# @capture`, `# @assert`, `# @shell`, `# @reset`). The features below exist in one
or both of those tools and are **not supported yet**. A plan using them stays
parseable — lazyhttp will simply ignore or pass through the unsupported part — but
it won't behave the way it does in the original tool, so watch for the footguns
called out below.

### Not supported yet

Each entry lists the upstream syntax and what lazyhttp does with it today.

- **`{{$dotenv VAR}}`** — the one dynamic variable not yet wired up; it needs
  `.env` file discovery. Left literal for now. (Other dynamic variables —
  `{{$uuid}}`, `{{$timestamp}}`, `{{$randomInt}}`, `{{$datetime}}`,
  `{{$processEnv}}` — are supported; see [Dynamic variables](#dynamic-variables).)
- **Inline response references** — `{{login.response.body.$.id}}`,
  `{{login.response.headers.X-Auth}}`.
  Not resolved; sent literally. Use `# @capture` instead.
- **Response-handler scripts** — `> {% client.test(...); client.global.set(...) %}`.
  Ignored (lines starting with `>` are dropped). Use `# @capture` / `# @assert`.
- **Pre-request scripts** — `< {% request.variables.set(...) %}`.
  Recognized as a script and ignored (not sent as a body). The variables it would
  set are not applied.
- **Per-request settings** — `# @no-redirect`, `# @no-cookie-jar`, `# @no-log`,
  `# @timeout 30 s`, `# @prompt {pw}`, `# @note`.
  Unrecognized directives are ignored (treated as plain comments).
- **Multipart / form-data uploads** — `Content-Type: multipart/form-data; boundary=...`
  with `< ./file` parts. The body text is sent as-is; file parts are not read or attached.
- **GraphQL requests** — header `X-REQUEST-TYPE: GraphQL` followed by a query and
  variables. No special handling; sent as a plain body.
- **Auth sugar** — `Authorization: Basic user pass`, `Digest`, `AWS`, Azure AD, OIDC.
  Sent verbatim — `Basic user pass` is not base64-encoded, so it goes on the wire
  un-encoded. Pre-encode it yourself.
- **`//` comment/directive prefix** — `// @name Foo`.
  Not recognized; lazyhttp directives require a `#` prefix.
- **Multi-line URLs** — continuation lines starting with `?` / `&`.
  Only the first line is read as the request line.
- **`$shared` / private env files** — `$shared` env, `http-client.private.env.json`.
  Only `http-client.env.json` is read; the named env is used as-is.

### Footguns worth remembering

- **`Basic` auth isn't encoded.** Provide the already-base64-encoded credentials
  (`Authorization: Basic dXNlcjpwYXNz`) rather than the `user pass` shorthand.

If you hit one of these and want it supported, it's worth opening an issue —
several (per-request directives, response references) are good candidates.
