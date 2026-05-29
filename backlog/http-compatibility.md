# Compatibility roadmap

An implementation plan for closing the highest-value gaps between lazyhttp and the
IntelliJ HTTP Client / VS Code REST Client formats. The gaps themselves are
catalogued in [docs/http-format.md](../docs/http-format.md#compatibility-notes);
this document is the *how*.

Ordering is by value-per-effort: each phase is independently shippable and leaves
the parser/executor in a working state. Phases 1–3 are the cheap, high-impact wins.

## Where the relevant code lives

- **`{{var}}` matcher + `Expand`** — `internal/httpfile/vars.go`: the `varPattern`
  regex and `Vars.Expand`, which does the substitution.
- **Directive parsing** — `internal/httpfile/parse.go`: `applyDirective` (called
  from `parseBlock`) and `parseHTTP` (which drops `>`/`<` body lines today).
- **Step model** — `internal/step/step.go`: the `Step` struct that new per-request
  options hang off.
- **Expansion at run time** — `internal/ui/model.go`: `Model.expand` resolves
  URL/headers/body before `exec.Run`.
- **HTTP execution** — `internal/exec/http.go`: `runHTTP` builds and sends the
  `*http.Request`.

Two structural facts shape everything below:

1. Variables are expanded at **execution time** in `Model.expand`, not at parse
   time — so anything generated per-send (dynamic variables) belongs in the
   expansion path, and anything captured can flow into it.
2. `parseHTTP` currently **discards** any body whose first non-blank line starts
   with `>` or `<` (`parse.go:219`). Several features below hook exactly there.

---

## Phase 1 — Dynamic variables (`{{$uuid}}`, `{{$timestamp}}`, …) — ✅ shipped in 0.3.0

**Value:** high — extremely common in real plans. **Effort:** small, self-contained.

Landed in `internal/httpfile/dynamic.go` with the widened `varPattern` in
`vars.go`. `$dotenv` is intentionally still deferred to Phase 4 (needs `.env`
discovery). The rest of this section is kept for reference.

### Changes

1. **Widen the matcher.** `varPattern` in `vars.go` is `\{\{\s*([\w.-]+)\s*\}\}`,
   which can't match a leading `$` or the space-separated args some dynamic vars
   take (`{{$randomInt 0 9}}`). Broaden the inner capture to allow `$` and spaces,
   e.g. `\{\{\s*(\$?[\w.-]+(?:\s+[^}]+)?)\s*\}\}`, then split the captured token
   into name + args inside `Expand`.

2. **Resolve dynamics before the user map.** In `Expand`, when the name starts with
   `$`, dispatch to a new `dynamic(name, args)` resolver; otherwise fall back to the
   current map lookup. A name that the resolver doesn't recognize stays literal
   (current "leave unknown untouched" behavior).

3. **New file `internal/httpfile/dynamic.go`** implementing the resolver:

   - `$uuid` / `$guid` — UUID v4 (use `crypto/rand`; no new dependency needed).
   - `$timestamp` — current Unix seconds.
   - `$isoTimestamp` — RFC 3339 UTC.
   - `$randomInt [min max]` — random int in `[min, max)`, default `0 1000`.
   - `$datetime <fmt>` — `rfc1123` / `iso8601` (defer custom Day.js-style formats).
   - `$processEnv <VAR>` — `os.Getenv(VAR)`.

### Caveats / tests

- Each `{{$uuid}}` occurrence should resolve **independently** (don't memoize), so
  two UUIDs in one request differ. `ReplaceAllStringFunc` already calls per match.
- Determinism in tests: inject the clock/RNG via an unexported package var so a test
  can stub them (`now func() time.Time`, `randInt func(n int) int`).
- Skip `$dotenv` for now — it needs `.env` file discovery; revisit with Phase 4.

---

## Phase 2 — Request body from a file (`< ./body.json`, `<@ ./file`) — ✅ shipped in 0.4.0

**Value:** high. **Effort:** small. Today these bodies are **silently dropped** —
the worst kind of gap because the request still sends, just with no body.

Landed via `BodyFile`/`BodyFileVars` on `step.Step`, `parseBodyRef` in
`parse.go`, and the file read in `Model.expand` (`ui/model.go`), which now
returns an error so a missing file surfaces as a failed result instead of an
empty body. The request preview shows `body from < path`. The rest of this
section is kept for reference.

### Changes

1. **Model.** Add to `step.Step` (`step.go`):
   ```go
   BodyFile     string // path from `< file` / `<@ file`; empty when inline
   BodyFileVars bool   // true for `<@` (expand {{vars}} in the file contents)
   ```

2. **Parse.** In `parseHTTP` (`parse.go`), replace the "drop `<` lines" branch:
   detect a body that starts with `<`, parse the form `< path`, `<@ path`,
   `<@encoding path`, and record `BodyFile`/`BodyFileVars` instead of discarding.
   Resolve the path relative to the **plan file's directory** (thread the base dir
   into `Parse`/`parseBlock`, or store it on the step and resolve at run time).

3. **Execution.** Read the file in `runHTTP` (`http.go`), or in `Model.expand`
   (which already has the var set) when `BodyFileVars` is set. Reading in `expand`
   keeps `runHTTP` pure and lets `{{vars}}` apply to file contents uniformly.
   Surface a read error as a failed result (reuse the `fail(err)` path).

### Caveats / tests

- `< file` does **not** expand `{{vars}}`; `<@ file` does. Keep that distinction.
- Encoding override (`<@latin1`) is rare — accept and ignore the encoding token
  initially (treat as UTF-8), or wire `golang.org/x/text/encoding` later.
- The request preview (`i`) should show "body from `<path>`" rather than nothing.

---

## Phase 3 — Per-request directives + `Basic` auth encoding — ✅ shipped

**Value:** medium-high. **Effort:** small. These fit lazyhttp's existing `# @`
design and remove two surprising behaviors.

> Shipped. `Basic` auth base64 encoding landed first in `internal/exec/basicauth.go`
> (`encodeBasicAuth`, applied in `runHTTP`), handling the `Basic user pass`,
> `Basic user:pass`, and pre-encoded forms. The two per-request directives followed:
> `# @timeout <n> <unit>` (`ms`/`s`/`m`, bare number = seconds) and `# @no-redirect`
> parse in `applyDirective` (`parse.go`, via `parseTimeout`) onto new `Timeout` /
> `NoRedirect` fields on `step.Step`; `clientFor` (`http.go`) builds a per-request
> client overriding just those fields when either is set, leaving the shared client
> otherwise. A `@no-redirect` step's `3xx` counts as success — `step.Result` carries
> a `NoRedirect` bit so `Result.OK()` widens its accepted range to `< 400` only for
> those steps. The TUI request preview shows an active `⚙ timeout … · no-redirect`
> line. The interactive `# @prompt` / `# @note` directives remain deferred. The rest
> of this section is kept for reference.

### Changes

1. **New directives** in `applyDirective` (`parse.go`), with matching fields on
   `step.Step`:

   - `# @no-redirect` → `NoRedirect bool` — use a client whose `CheckRedirect`
     returns `http.ErrUseLastResponse`.
   - `# @timeout <n> <unit>` → `Timeout time.Duration` — per-request client timeout
     overriding the shared 30s.

   Parse `@timeout` value+unit (`ms`/`s`/`m`, default seconds) in a small helper.
   Because `runHTTP` uses a single shared `httpClient`, build a per-request client
   when either option is set; otherwise keep using the shared one.

2. **`Basic` auth sugar.** In `runHTTP` (after headers are set), if an
   `Authorization` header has the form `Basic <user> <pass>` (two space-separated
   tokens, not already base64), rewrite it to `Basic base64(user:pass)` via
   `req.SetBasicAuth`. Leave a single-token value (`Basic dXNlcjpwYXNz`) untouched.
   Do the same recognition for `Digest` only if/when digest is implemented —
   otherwise leave it verbatim and document it as unsupported.

3. **Defer interactive directives.** `# @prompt` and `# @note` need TUI input flow
   (a modal in `internal/ui`); list them as a follow-up rather than bundling here.

### Caveats / tests

- Unknown directives must keep being ignored (don't break forward-compat).
- Test the `Basic` rewrite against both the shorthand and the pre-encoded forms.

---

## Phase 4 — Larger items (scoped, not yet committed)

These are real but higher-effort; capture them so they aren't rediscovered.

- **Inline response references** (`{{step.response.body.$.id}}`) — ✅ shipped.
  Resolved in `Model.resolveResponseRef` (`ui/model.go`), threaded into expansion
  via the new `Vars.ExpandFunc` hook (`vars.go`) so the matcher stays in one place;
  `varPattern` was widened to carry JSON-path punctuation through the token. Maps
  `name.response.body.<path>` / `name.response.headers.<Name>` onto a `capture.Eval`
  expression against the named step's most recent result. `# @capture` still covers
  the same need and reads better in long chains.
- **`http-client.private.env.json`** — ✅ shipped. `loadEnvFile` (`vars.go`)
  finds the env directory by walking up parent dirs (stopping at the `.git`
  boundary), then layers the private file over the shared one per-variable via
  `mergeEnvs`, so secrets like `clientSecret` resolve while a shared `Security`
  block stays intact.
- **`$dotenv` / `$shared`.** Still pending. Extend `vars.go` loading: read a
  sibling `.env`, and merge a `$shared` pseudo-environment into every env.
- **Multipart / form-data file parts.** Parse `multipart/form-data` bodies and
  stream `< ./file` parts via `mime/multipart` in `runHTTP`. Meaningful parser +
  executor work.
- **GraphQL** (`X-REQUEST-TYPE: GraphQL`). Detect the header, wrap the
  query+variables body into the `{"query":…,"variables":…}` JSON envelope.
- **Response-handler / pre-request JS** (`> {% … %}`, `< {% … %}`). Full support
  means an embedded JS engine — almost certainly out of scope. Minimum viable
  behavior: parse and skip the `{% … %}` blocks cleanly (don't treat them as body),
  since `# @capture`/`# @assert` already cover the common cases.

---

## Suggested sequencing

1. **Phase 1** — dynamic variables (widen `varPattern`, add `dynamic.go`).
2. **Phase 2** — `< file` bodies (stop the silent-drop footgun).
3. **Phase 3** — per-request directives + `Basic` encoding.
4. Reassess Phase 4 against actual user-reported plans.

Each phase should land with parser tests in `internal/httpfile` and, for Phase 3,
an executor test for the `Basic` rewrite. Update
[docs/http-format.md](../docs/http-format.md) — move each shipped item out of "Not
supported yet" and into the main reference as it lands.
