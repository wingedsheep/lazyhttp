# Headless CI runner (`lazyhttp run`)

The single highest-leverage feature missing from lazyhttp. Today the only way to
execute a plan is the interactive TUI; there is no way to run a `.http` file
non-interactively and get a pass/fail exit code. A headless runner is the #1
repeated ask across every comparable tool (httpYac's `httpyac send`, Slumber's
`slumber request`, Hurl, and the long-standing VS Code REST Client "give us a
CLI + tests" issue) — and it's what turns lazyhttp from "a nice TUI" into "a
thing teams put in their pipeline." Requests already version-controlled as code
become runnable in CI with no new file format.

This is **Tier 1** from the feature brainstorm. The other two Tier 1 items —
`Basic`-auth base64 encoding and per-request `@timeout` / `@no-redirect` — are
already scoped in
[http-compatibility.md](./http-compatibility.md#phase-3--per-request-directives--basic-auth-encoding)
(Phase 3); they compound well with this runner (auth + timeouts matter most in
automation) but aren't re-scoped here.

## The core problem: the engine is entangled with the UI

The execute→evaluate→capture→chain loop currently lives inside `internal/ui` and
`exec` is bound to Bubble Tea:

- **Execution returns a `tea.Cmd`.** `exec.Run` (`internal/exec/exec.go:25`)
  dispatches to `runHTTP`/`runShell`, both of which return `tea.Cmd` producing a
  `ResultMsg` — designed to run off the UI thread, not to be called synchronously.
- **The orchestration logic hangs off `ui.Model`:**
  - `expand` (`internal/ui/model.go:521`) — resolves `{{vars}}`, dynamic vars,
    inline response refs, and reads body-from-file relative to the plan dir.
  - `evaluate` (`model.go:599`) — runs `@capture` into the var map and `@assert`
    against the result.
  - `resolveResponseRef` / `lastResult` (`model.go:561`, `:587`) — inline
    `{{name.response.body.$.x}}` resolution.
  - `resetState` (`model.go:617`) — `@reset` semantics: clear results, drop
    captures back to `baseVars`.
  - The var lifecycle itself (`vars` = env + inline `@defs` + captures, snapshot
    in `baseVars`) is built in `ui.New`.

So the runner can't just "call the engine" — there isn't a UI-independent engine
yet. **The first and most valuable step is to extract one.** That refactor also
de-risks the UI (pure logic becomes unit-testable without a Bubble Tea harness).

---

## Phase 1 — Extract a UI-independent engine (`internal/runner`)

**Value:** high (prerequisite for everything below; also hardens the TUI).
**Effort:** medium — mostly moving code, not writing new logic.

### Changes

1. **Add a synchronous execution entry point in `exec`.** Factor the bodies of
   `runHTTP`/`runShell` into pure functions returning `(step.Result, error)` with
   no `tea` dependency, e.g. `exec.Do(s step.Step) step.Result`. Keep the existing
   `Run` (the `tea.Cmd` wrapper) as a thin adapter over it so the TUI is
   unchanged. This removes the only hard Bubble Tea coupling in the execution path.

2. **New package `internal/runner`** owning the plan-level loop, lifted from
   `ui.Model` and made UI-agnostic:
   - A `Plan` value: parsed `[]step.Step`, the plan-file dir (for `<` body paths),
     the var set, and `baseVars`.
   - `expand`, `evaluate`, `resolveResponseRef`/`lastResult`, and `resetState`
     move here (or to a shared helper both `ui` and `runner` call). The TUI then
     calls into `internal/runner` instead of holding the logic itself — single
     source of truth for the variable lifecycle.
   - `RunAll(ctx) ([]step.Result, error)` executes every step top-to-bottom,
     threading captures forward exactly as `onResult` does today, honoring
     `@reset` and stopping a chain on failure where the TUI would.

3. **Keep `ui.Model` as a consumer.** `ui` should construct a `runner.Plan` and
   delegate, rather than duplicating expand/evaluate. This is the refactor that
   pays for itself: today a bug in capture semantics has to be reproduced through
   the TUI.

### Caveats / tests

- This is the risky phase: it touches the heart of `model.go`. Land it behind a
  green `go test ./...` with the existing UI behavior unchanged — the TUI is the
  regression test. Add direct `internal/runner` tests for a multi-step
  capture→assert chain, a `@reset` step, and a failing-assertion stop.
- Preserve "unknown `{{vars}}` left untouched" and the execution-time expansion
  ordering exactly; these are load-bearing behaviors documented in CLAUDE.md.

---

## Phase 2 — The `run` subcommand + exit codes

**Value:** high. **Effort:** small once Phase 1 lands.

### Changes

1. **Subcommand dispatch in `main.go`.** Today `main` always launches the TUI
   (`main.go:42-49`). Introduce a verb: `lazyhttp run <plan.http>` executes
   headlessly; bare `lazyhttp <plan.http>` keeps launching the TUI (back-compat).
   Reuse the existing `--env` flag; `--theme` is irrelevant to `run`.
2. **Drive `internal/runner` and report.** Run the plan, print a per-step line
   (method → status · duration, with ✓/✗ for each assertion) and a final summary
   (`N passed, M failed`).
3. **Exit codes** — the whole point for CI:
   - `0` — all steps OK and all assertions passed.
   - `1` — any transport error, non-OK status that an assertion caught, or failed
     assertion.
   - `2` — usage / parse / unreadable-plan errors (matches current `os.Exit(2)`).
4. **Selection flags** (small, high-value for pipelines):
   - `--filter <substr>` — run only matching steps (reuse the TUI's match logic).
   - default runs the full plan top-to-bottom (the "run all" the TUI lacks too).

### Caveats / tests

- A failed `@assert` must produce a **non-zero** exit even though the HTTP call
  itself succeeded — that's the behavior CI depends on. Test it explicitly.
- Respect `@reset` in headless runs the same as the TUI, or document that `run`
  executes linearly and `@reset` clears captured state mid-plan as usual.

---

## Phase 3 — CI-friendly output formats

**Value:** high for adoption. **Effort:** small–medium.

### Changes

1. **`--output` / `-o` formats:**
   - `pretty` (default) — the human summary from Phase 2, colored when stdout is a
     TTY, plain otherwise (detect with `term.IsTerminal`).
   - `json` — a machine-readable report (per-step method/URL/status/duration,
     captures, assertion outcomes, overall pass/fail) for scripting and dashboards.
   - `junit` — JUnit XML, so the run drops straight into GitHub Actions / GitLab
     test reporting. This is what makes teams actually adopt it; each step (or each
     assertion) maps to a `<testcase>`.
2. **`--quiet`** — suppress per-step lines, print only the final summary + exit
   code (good for noisy CI logs).

### Caveats / tests

- Keep JSON/JUnit output on stdout and diagnostics on stderr so redirection in CI
  is clean.
- Golden-file tests for each formatter against a fixed multi-step result set.

---

## Phase 4 — Polish & docs

- **`--timeout` global override** until per-request `@timeout` (compat Phase 3)
  lands; afterward, per-request wins.
- **Secrets hygiene:** ensure `--output json` doesn't echo `Authorization`
  headers / captured tokens by default, or provide `--redact`.
- **Docs:** a "Running in CI" section in `docs/http-format.md` (or README) with a
  copy-paste GitHub Actions snippet:
  `lazyhttp run api/*.http --env ci --output junit > report.xml`.
- **Glob / multiple plans** (`lazyhttp run api/*.http`) — natural once single-file
  `run` works; shell-expanded args, aggregate exit code.

---

## Suggested sequencing

1. **Phase 1** — extract `internal/runner`; keep the TUI green. The enabling
   refactor; do it carefully and it pays back immediately in testability.
2. **Phase 2** — `run` subcommand + exit codes. Minimum viable CI runner.
3. **Phase 3** — JSON + JUnit output. The adoption unlock for teams.
4. **Phase 4** — globs, secrets hygiene, docs.

Phases 1–2 are the must-haves (headless execution with a meaningful exit code);
Phase 3 is what makes it *stick* in real pipelines. Compose well with
[http-compatibility.md](./http-compatibility.md) Phase 3 (auth + per-request
timeouts) and [windows-support.md](./windows-support.md) (CI runners are often
Windows).
