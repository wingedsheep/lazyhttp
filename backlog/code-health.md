# Code health & maintainability

Unlike the rest of the backlog — which adds features — this document captures
**internal refinements**: structural, robustness, and polish work that keeps the
codebase healthy as it grows. Nothing here is broken; the tree builds green and
`go test ./...` / `go vet ./...` are clean. These are the things worth doing
*before* they become friction, not fixes for present bugs.

The codebase is in good shape overall: a strict one-directional data flow
(`parse → expand → execute → evaluate → capture`), a correct UI-thread /
off-thread boundary, idle-by-design rendering, and high comment quality. The
items below are ordered by value-per-effort within three tiers.

## Where the relevant code lives

- **The god-object** — `internal/ui/model.go` (~800 lines): owns the parsed
  steps, the variable lifecycle (`expand`, `evaluate`, `resetState`,
  `resolveResponseRef`, `lastResult`, `cloneVars`), input routing
  (`onKey`/`listKey`/`filterKey`/`envKey`), the run-from-here chain, and the
  cursor/filter/viewport controller.
- **Execution** — `internal/exec`: `Run` (`exec.go`) returns a `tea.Cmd`
  producing one `ResultMsg`; `runHTTP` (`http.go`), `runShell` (`shell.go`).
- **Per-refresh rendering** — `internal/ui/view.go`: `formatResult` rebuilds the
  request/response preview on every viewport refresh.
- **Auth resolution** — `internal/auth/auth.go`: `Resolver.Resolve` mutates the
  step in place; `internal/ui/model.go` `authResolver` expands configs on the UI
  thread.
- **JSON path** — `internal/capture/capture.go`: `tokenize` / `jsonPath`.

---

## Tier 1 — Structural (worth doing)

### 1. Decompose `model.go`

**Value:** high (the one file that keeps growing). **Effort:** medium —
mostly moving code.

`model.go` is the parser-consumer, the variable lifecycle manager, the input
router, the chain driver, *and* the list/viewport controller. It's still
readable, but it's where complexity accretes. Extract along seams:

- Variable/state lifecycle (`expand`, `evaluate`, `resetState`,
  `resolveResponseRef`, `lastResult`, `cloneVars`) into a small state type.
- Filter/cursor navigation (`visible`, `moveCursor`, `snapCursor`,
  `setTop`/`setBottom`) into a list-controller.

> **Overlap:** this is largely subsumed by the `internal/runner` extraction in
> [headless-ci-runner.md](./headless-ci-runner.md#phase-1--extract-a-ui-independent-engine-internalrunner)
> — that refactor moves the variable lifecycle out of `ui.Model` wholesale and
> makes it unit-testable without a Bubble Tea harness. **Prefer doing the engine
> extraction first**; what remains here afterward is just splitting the residual
> input-routing / list-controller code. Don't do both independently.

### 2. Make the "single in-flight step" invariant explicit

**Value:** medium-high (latent correctness). **Effort:** small.

The entire off-thread design is safe *only because exactly one step runs at a
time* — the run-from-here chain fires the next step only after the previous
`ResultMsg` arrives. But nothing enforces it: `Model.run` doesn't guard against
being invoked while a step is already in flight, so running step A then step B
before A returns launches two concurrent goroutines. They write disjoint
`results[i]` slots (probably fine today), but the shared `auth.Cache` and the
`vars` map (captures from two in-flight responses interleaving in `evaluate`)
are the real exposure.

Either (a) document "single in-flight step" as an explicit invariant where
`run` and `onResult` live, or (b) guard `run` to no-op while `anyRunning()`.
Option (b) is the safer default — and interacts cleanly with a future
"run all" key ([differentiators.md](./differentiators.md) Feature C step 1).

### 3. Cache the expanded step to avoid per-refresh file re-reads

**Value:** medium. **Effort:** small.

`formatResult` (`view.go`) calls `m.expand(m.steps[i])` on every viewport
refresh. For inline bodies that's cheap, but when the request preview is shown
(`i`) and the selected step has a `< file` / `<@ file` body, the file is
**re-read from disk on every keystroke/scroll**. Cache the expanded step
alongside the existing `names` / `bodyView` caches (rebuilt on
load/result/reset), or read the body lazily once per selection.

---

## Tier 2 — Robustness & UX (nice to have)

### 4. Deterministic header order

**Value:** medium. **Effort:** small.

`expand` (`model.go`), `auth.Resolver.Resolve` (`auth.go`), and the request
preview (`view.go`) all range over `map[string]string`, so the **preview**
renders headers in a different order on each redraw — mildly jarring. Sort keys
in the preview loop to stabilize it. (Related limitation worth a doc note:
duplicate headers collapse to one, since `Step.Headers` is a map.)

### 5. Cap response size

**Value:** medium. **Effort:** small.

`runHTTP` does `io.ReadAll(resp.Body)` (`http.go`) with no limit, then
pretty-prints and highlights the whole thing. A very large response spikes
memory and stalls the highlighter. Wrap the body in an `io.LimitReader` with a
sane cap and surface a "truncated" indicator in the result pane.

### 6. Unify the "is this JSON?" heuristic

**Value:** low-medium. **Effort:** small.

`prettyJSON` (`shell.go`) keys off `Content-Type` containing `"json"`, while
`highlightJSON` (`json.go`) independently re-sniffs by first character
(`{`/`[`). The two can disagree — e.g. a JSON body served as `text/plain` gets
highlighted but not pretty-printed. Pick one source of truth and thread it
through.

### 7. Stricter JSON-path array indices

**Value:** low. **Effort:** small.

`capture.tokenize` silently drops a non-numeric array index (`[abc]` produces no
token), so a typo'd capture path can resolve to the *wrong* value rather than
reporting "no match". Treat a malformed index as an unresolved path.

---

## Tier 3 — Polish

Captured so they aren't rediscovered:

- **Magic number in `pageStep`** (`model.go`) — the half-page jump hardcodes
  `(m.height-6)/2`; the `6` is chrome height computed properly in `layout`.
  Derive it from `contentH` instead.
- **In-place header mutation comment** — `auth.Resolver.Resolve` mutates
  `s.Headers` in place while `runHTTP` reads the same map. Safe today (the step
  is a per-run copy from `expand`), but worth a one-line comment noting the
  coupling.
- **`config` path uses `lazy-http`** (the repo dir) while everything else is
  `lazyhttp` (`config.go`) — intentional per CLAUDE.md, but a one-line comment
  at the path helper would prevent future "fix the typo" churn.
- **`indexOf` returns 0 (not -1) on miss** (`model.go`) — fine for its single
  env-picker caller, but the name implies standard semantics; rename or comment
  at the call site.
- **Input-routing tests** — `model_test.go` exists, but the keymap dispatch
  (`onKey`/`filterKey`/`envKey`) is the most logic-dense untested area. Add
  table tests once Tier 1 #1 (or the engine extraction) makes the routing layer
  addressable on its own.

---

## Suggested sequencing

1. **Tier 1 #2** (in-flight guard) and **#3** (expand caching) — small, safe,
   immediately worthwhile; no dependency on the larger refactor.
2. **`internal/runner` extraction** ([headless-ci-runner.md](./headless-ci-runner.md)
   Phase 1) — do this *before* Tier 1 #1; it subsumes most of the `model.go`
   split and is the higher-value move.
3. **Tier 1 #1 residual** — split the leftover input-routing / list-controller
   code once the engine is out.
4. **Tier 2** items as they're felt (large-response cap first if anyone hits a
   big payload).
5. **Tier 3** alongside whatever touches the same files.

None of this is urgent — the Tier 1 items are about keeping the codebase
maintainable as it grows, not fixing anything broken.
