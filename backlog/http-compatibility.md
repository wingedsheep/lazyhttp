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

## Phase 2 — Request body from a file (`< ./body.json`, `<@ ./file`)

**Value:** high. **Effort:** small. Today these bodies are **silently dropped** —
the worst kind of gap because the request still sends, just with no body.

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

## Phase 3 — Per-request directives + `Basic` auth encoding

**Value:** medium-high. **Effort:** small. These fit lazyhttp's existing `# @`
design and remove two surprising behaviors.

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

- **Inline response references** (`{{step.response.body.$.id}}`). lazyhttp already
  has `# @capture` covering the same need; this would let plans authored for
  VS Code work unmodified. Requires keeping named results addressable and teaching
  `Expand` (or a pre-pass) to resolve `name.response.…` against stored results.
- **`$dotenv` / `$shared` / `http-client.private.env.json`.** Extend `vars.go`
  loading: read a sibling `.env`, merge `$shared` into every env, and layer the
  private env file over `http-client.env.json`.
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
