# Tier 2 — strong differentiators

Three features that move lazyhttp from "a clean `.http` runner" to "the one I
reach for." Unlike the Tier 1 [headless runner](./headless-ci-runner.md) (table
stakes for CI) these are about doing things competitors do poorly or not at all
in a terminal: real **auth flows**, **streaming responses**, and **plan
composition**. Each is independently shippable; ordering below is by
value-per-effort.

These build on the same code the rest of the backlog touches:

- **Directive parsing** — `internal/httpfile/parse.go:142` (`applyDirective`),
  which dispatches `# @...` lines. New directives (`@auth`, `@run`) hook here.
- **Step model** — `internal/step/step.go:21` (`Step`) and `:78` (`Result`). New
  per-step options and result shapes hang off these.
- **Execution** — `internal/exec`: `Run` (`exec.go:25`) returns a `tea.Cmd`
  producing **one** `ResultMsg`. Streaming breaks that one-shot assumption; auth
  needs a pre-request hook.
- **Var lifecycle** — env + inline `@defs` + captures, expanded at run time in
  `ui/model.go`. An auth token is just another captured variable.

> Note: this Tier assumes the Tier 1 engine extraction (`internal/runner`) is
> either done or close. Auth and `@run` belong in that shared engine so they work
> identically in the TUI and headless `run`. If the engine isn't extracted yet,
> implement against `ui.Model` and migrate — but prefer doing Tier 1 Phase 1
> first.

---

## Feature A — OAuth2 / bearer-token auth helper — ✅ shipped in 0.8.0

**Value:** very high — auth is the #1 reason people fall back to Postman.
**Effort:** medium.

> Shipped, but via the **native IntelliJ `Security.Auth` env format** rather than
> a new `# @auth` directive (below): a request references a configuration with
> `{{$auth.token("id")}}` / `{{$auth.idToken("id")}}`, keeping plans portable to
> the IntelliJ HTTP Client and secrets in the (often git-ignored) env file.
> Implemented in `internal/auth` (`Config`, a thread-safe expiry-honoring token
> `Cache`, and a `Resolver`), parsed by `httpfile.LoadAuth`, and applied off the
> UI thread via the new `exec.AuthResolver` hook in `runHTTP`. Grant types:
> **Client Credentials** and **Password**; the interactive grants (point 4) are
> still deferred. The `# @auth bearer/basic` directive sugar below was **not**
> implemented — `Authorization: Bearer {{token}}` already covers bearer, and the
> `Basic` base64 rewrite remains [compat Phase 3](./http-compatibility.md#phase-3--per-request-directives--basic-auth-encoding).
> The original directive-based design is kept below for reference.

Today `Authorization` is whatever literal string you type, and `Basic user pass`
isn't even base64-encoded (a documented footgun; the fix is
[compat Phase 3](./http-compatibility.md#phase-3--per-request-directives--basic-auth-encoding)).
Real APIs want a token fetched from a token endpoint and attached as
`Authorization: Bearer …`. The win: lazyhttp does the token dance once and reuses
it, instead of the user hand-rolling a "login" step + `@capture` every plan.

### Changes

1. **New directive `# @auth`** in `applyDirective` (`parse.go:142`), with a
   matching `Auth` field on `step.Step`. Start with the two that cover most cases:
   - `# @auth bearer {{token}}` — sugar for `Authorization: Bearer <expanded>`.
   - `# @auth oauth2 client_credentials` with companion directives or inline
     `@def`s for `token_url`, `client_id`, `client_secret`, `scope`. On run, POST
     the client-credentials grant, parse `access_token`, attach it as
     `Authorization: Bearer …`.
2. **Token acquisition + caching.** Implement in the engine (so TUI and headless
   share it): before sending a step whose `Auth` needs a token, fetch-or-reuse.
   Cache by (token_url, client_id, scope) with `expires_in` honored, so a plan of
   20 requests does one token fetch. The cached token is effectively a captured
   var — store it in the var map so `{{$auth.token}}` style refs and `@capture`
   interop work.
3. **`@auth basic user pass`** — fold the base64 encoding here too, superseding /
   complementing the compat-Phase-3 `Basic` rewrite (one place that owns auth
   header construction).
4. **Defer the interactive grants.** Authorization-code / PKCE / device-code need
   a browser round-trip and a local callback listener — real, but a bigger lift
   and awkward headless. Scope client-credentials + bearer + basic first; list the
   interactive grants as a follow-up.

### Caveats / tests

- **Secrets:** `client_secret` should come from `{{$processEnv …}}` / env file,
  never be printed in the request preview or (later) JSON output. Redact the
  `Authorization` header in previews by default.
- Token-refresh on 401 is a nice-to-have; start with expiry-based refresh only.
- Test the client-credentials flow against a stub token endpoint (table test in
  `internal/exec` or the new engine): first call fetches, second reuses, expired
  re-fetches.

---

## Feature B — Streaming responses (SSE, chunked, NDJSON)

**Value:** high and **underserved** — SSE/streaming is a top, long-unaddressed
VS Code REST Client request, and a headline feature for newer TUIs. A terminal UI
is actually a *great* place to watch a stream tick in. With LLM/event APIs
everywhere, this lands well.

### The core problem: execution is one-shot

`exec.Run` (`exec.go:25`) returns a `tea.Cmd` that produces exactly one
`ResultMsg`, and `step.Result.Body` is a single finished string. Streaming means
**many** updates over the life of one request. This needs a new message path, not
a tweak.

### Changes

1. **Detect stream-worthy responses.** Either by response `Content-Type`
   (`text/event-stream`, `application/x-ndjson`) or an explicit
   `# @stream` directive on the step (most predictable; add to `applyDirective`).
2. **Incremental delivery in `exec`.** Add a streaming runner that reads
   `resp.Body` progressively and emits `tea.Msg`s as chunks arrive — e.g. a
   `StreamChunkMsg{Index, Data}` plus a terminal `ResultMsg`. In Bubble Tea this
   is the standard "long-running command pumping messages" pattern (a channel
   drained by a subscription `tea.Cmd`). Provide a `context` so navigating away /
   pressing a key can cancel the request.
3. **UI: live append.** The result pane appends chunks to a growing buffer and
   keeps the viewport pinned to the bottom (or follows scroll) while running, with
   the existing spinner indicating "still streaming." For SSE, parse
   `event:`/`data:` framing and render events as they arrive.
4. **Captures/asserts on streams.** Decide and document semantics: simplest is to
   evaluate `@capture`/`@assert` against the **accumulated** body once the stream
   closes (reuses today's engine). A later enhancement: per-event assertions.
5. **Headless behavior.** In `lazyhttp run`, stream to stdout line-by-line and
   apply assertions on close — naturally CI-friendly.

### Caveats / tests

- Don't run streamed bodies through `prettyJSON` wholesale (`exec/shell.go:52`) —
  that buffers and reformats; stream raw, pretty-print only non-streaming JSON.
- Backpressure / huge streams: cap the retained buffer (e.g. last N KB / lines)
  and `log`-style note truncation rather than growing unbounded.
- Cancellation must close the body and stop the goroutine; test that navigating
  away mid-stream doesn't leak.
- Test the SSE framing parser against a fixture (multi-line `data:`, comments,
  `retry:`), independent of the network.

---

## Feature C — Plan composition (`@run` / include + run-all)

**Value:** high for real projects (shared setup/teardown, reused login flows).
**Effort:** medium. Directly answers REST Client's "import/require .http file" and
"run multiple requests in sequence" asks.

### Changes

1. **"Run all" first (cheap, standalone).** The TUI has run-one (`enter`) and
   run-from-here (`a`) but no run-the-whole-plan. Add a key (e.g. `A` or `R`) that
   runs every step top-to-bottom with the same chain semantics as `a`. This is the
   same loop the headless runner needs — share it via `internal/runner`.
2. **`# @run path/to/other.http[#stepName]`** in `applyDirective` (`parse.go:142`):
   a step that, instead of an HTTP/shell body, executes another plan (or a named
   step within it). Use cases: a `setup.http` login that captures a token, reused
   across plans.
   - **Variable scope:** captures from the included plan flow back into the caller's
     var map (that's the point — `setup.http` sets `{{token}}`). Document the
     direction explicitly; it's the subtle part.
   - **Path resolution:** relative to the *including* file's directory (same rule
     as body-from-file in `model.go:540`).
3. **Plain include of definitions.** A lighter variant: `# @import vars.http`
   pulls just the `@def` variable definitions from another file (no requests),
   covering the "import variables from a separate file" request without running
   anything. Decide whether this is a separate directive or `@run` of a
   request-less file.

### Caveats / tests

- **Cycle detection:** `a.http` → `b.http` → `a.http` must error, not hang. Track
  the include stack; fail with a clear message.
- **Parser threading:** `Parse` currently works on one file's text; includes mean
  the parser (or the engine) needs the base directory and a resolver to load
  siblings. Cleanest is to resolve `@run` at execution time in the engine (it
  already knows the plan dir), keeping `parse.go` a pure single-file parser that
  just records the `@run` target.
- How included steps surface in the TUI list (inline-expanded vs a single
  collapsed "run setup.http" row) is a UX decision — start with the single
  collapsed row showing the included plan's pass/fail summary; expand later.

---

## Suggested sequencing

1. **Run-all** (Feature C step 1) — tiny, immediately useful, and it's the loop
   the headless runner reuses. Do it alongside Tier 1.
2. **OAuth2 / bearer helper** (Feature A) — highest standalone value; removes the
   biggest "fall back to Postman" reason.
3. **Streaming** (Feature B) — the showy differentiator; needs the new
   multi-message exec path, so it's the most architectural of the three.
4. **`@run` includes** (Feature C steps 2–3) — compose plans once setup/teardown
   reuse is actually felt.

Cross-references: auth + `@run` should live in the
[Tier 1 engine](./headless-ci-runner.md) so they work in both TUI and CI; auth
also subsumes the `Basic` rewrite from
[compat Phase 3](./http-compatibility.md). Streaming's cancellation/context work
overlaps nothing else and can proceed in parallel.
