# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`lazyhttp` is a terminal UI (Bubble Tea) for stepping through `.http` test plans — the
IntelliJ HTTP Client / VS Code REST Client format. Open a `.http` file, run requests one
at a time or in a chain, capture values from responses into variables, and assert on
responses. The module path is `github.com/wingedsheep/lazyhttp` (note: the repo directory
is `lazy-http` but the binary and module are `lazyhttp`).

## Commands

```sh
go build -o bin/lazyhttp .       # build (output is gitignored)
./bin/lazyhttp example.http      # open the TUI against the bundled example plan
./bin/lazyhttp .                 # folder mode: browse every .http/.rest plan under a dir
./bin/lazyhttp run example.http  # run headlessly (CI); exit 0 pass / 1 fail / 2 usage
go test ./...                    # all tests
go test ./internal/httpfile/     # one package
go test ./internal/ui/ -run TestLayout   # one test by name
go vet ./...
```

Go 1.24 (pinned via `mise.toml`). Releases are cut by GoReleaser (`.goreleaser.yaml` +
`.github/workflows/release.yml`) on a tag push, producing prebuilt binaries, a Homebrew
cask in `wingedsheep/homebrew-tap`, and the `install.sh` curl one-liner.

## Architecture

The data flows in one direction: **parse → expand → execute → evaluate → capture back into vars**.

- **`internal/step`** — the shared data model (`Step`, `Result`, `Capture`, `Assertion`).
  No logic beyond `Result.OK()` / `AssertsPass()`. Every other package depends on this one.
  A `Step` is either `KindHTTP` or `KindShell`. Step fields hold **raw `{{var}}` templates**;
  they are deliberately *not* expanded at parse time.

- **`internal/httpfile`** — parses `.http` files into `[]step.Step` (`parse.go`) and resolves
  variables (`vars.go`). Files split on `###` separators into blocks; each block has leading
  `# @...` directives (`@name`, `@group`, `@capture`, `@assert`, `@shell`, `@reset`), then an
  HTTP request line / headers / body, or a shell script. `@var = value` definitions are
  harvested in a first pass so they resolve regardless of position. `http-client.env.json`
  next to the plan supplies named environments. `Vars.Expand` leaves unknown `{{vars}}`
  untouched on purpose so the user can see what failed to resolve. `discover.go` powers
  folder mode: `DiscoverPlans(dir)` walks a tree for `.http`/`.rest` files (skipping dot-dirs,
  `node_modules`, `vendor`) into a sorted `PlanIndex`; `CountSteps(path)` parses one file for
  its step count (lazily, per visible row).

- **`internal/exec`** — runs a single step. `Do(step, auth)` executes it synchronously and
  returns a `step.Result` (the UI-independent entry point); `Run` wraps `Do` as a `tea.Cmd`
  that runs off the UI thread and delivers a `ResultMsg{Index, Result}` when done (so the TUI
  never blocks). `http.go` does the HTTP request (shared 30s-timeout client, pretty-prints
  JSON bodies); `shell.go` runs the body via `$SHELL -c`, capturing combined stdout+stderr
  and the exit code.

- **`internal/runner`** — the UI-independent execution engine. A `Plan` owns the parsed
  steps, per-step results, the plan-file dir, and the variable set (`Vars` + the pristine
  `BaseVars` baseline). The whole variable lifecycle lives here as methods on `*Plan`:
  `Expand` (resolve `{{vars}}`, dynamic vars, inline response refs, and `< file` bodies),
  `Evaluate` (run `@capture` into `Vars` and `@assert` against the result),
  `ResolveResponseRef`/`LastResult`, `Reset` (`@reset` semantics), and `AuthResolver`.
  `Run(ctx, include)` executes the plan top-to-bottom (capture→assert→reset, stop on the
  first non-OK step), running only the steps `include(i)` selects — `RunAll(ctx)` is the
  `include == nil` (run-everything) case. The headless `lazyhttp run` subcommand (`run.go`
  in package `main`) drives this and maps the outcome to a CI exit code; `--filter` builds
  the `include` predicate. `run.go` builds a UI-independent `runReport`, which `report.go`
  renders as `pretty` (TTY-coloured via `go-isatty`), `json`, or `junit` (`--output`/`-o`,
  `--quiet`). `internal/ui` constructs a `Plan` and delegates too, so capture/assert/reset
  semantics live in exactly one place. `Unresolved(s)` reports any `{{var}}` still present in
  an expanded HTTP step (excluding `$…` dynamic/auth tokens and `…response…` refs, which are
  filled later); both the TUI and headless `Run` use it to fail a step with a clear
  `UnresolvedError` instead of sending a literal-brace request.

- **`internal/capture`** — evaluates capture/assert expressions against a `Result`:
  `status`, `body`, `header.Name`, or a JSON path (`json.a.b[0].c`, `$.a`, or bare `a.b`).
  `Eval` is used for both captures (store into vars) and assertion left-hand sides.

- **`internal/auth`** — the OAuth2 helper. Parses the env file's IntelliJ-style
  `Security.Auth` blocks into `Config`s (via `httpfile.LoadAuth`); a `Resolver` then
  substitutes `{{$auth.token("id")}}` / `{{$auth.idToken("id")}}` placeholders in a
  step's URL/headers/body by fetching a token from the config's Token URL. A
  thread-safe `Cache` reuses tokens until `expires_in` lapses. Three grants are
  implemented: Client Credentials and Password (single back-channel POST) and
  Authorization Code (`authcode.go` — the interactive browser round-trip with PKCE
  by default; `browser.go` shells out to open the browser like `internal/clipboard`).
  Authorization Code persists its refresh token through an injected `RefreshStore`
  (`config.TokenStore` writes `tokens.json` 0600) so the browser login happens once;
  the `Cache` carries an `interactive` flag (true only in the TUI) so the headless
  `run` renews from a saved token or fails with a clear error instead of opening a
  browser. Wired in through `runner.Plan.AuthResolver`, which expands config values
  (so secrets resolve without racing the request goroutine) and hands a `Resolver` —
  satisfying `exec.AuthResolver` — to the executor; only the token fetch runs
  off-thread. `runner.Plan.NeedsInteractiveLogin` lets the TUI show a
  "waiting for browser sign-in" notice before a step that will open the browser.

- **`internal/ui`** — the Bubble Tea root. `model.go` is the heart: it holds a
  `*runner.Plan` (the steps, results, and variable lifecycle) and drives it, owning the
  cursor, filter, run-from-here chain, and the rendering caches. The plan, not the model,
  is the source of truth for steps/results/vars. `view.go` renders the two-pane layout,
  `styles.go`/`json.go` handle theming and JSON highlighting, `keys.go` defines the keymap.
  **Folder mode** adds two files: `browser.go` is the overview — a filterable, grouped list
  of the discovered `PlanIndex` (its own `tea.Model`-shaped `Update`/`View`, reusing the step
  list's navigation, `/` filter, and windowed scrolling) — and `app.go` is the root `App`
  model that stacks the browser and a single open plan. `App` is used only when the CLI arg is
  a directory (`main.go` stats it); a file argument still constructs `Model` directly. The
  browser emits `openPlanMsg` on select; `App` opens the plan fresh via `ui.New`, and a k9s-
  style `:` command bar (`:files`/`:plans`/`:ls`) or `Esc` returns to the overview. The
  browser keeps its cursor/filter across trips, so going back is instant. Plan-only messages
  (`spinner.TickMsg`, `exec.ResultMsg`) are forwarded to the plan even while the overview is
  foreground, so an in-flight request still finishes. `App` also carries the selected
  environment across opens (a plan's `E` picker writes back to `App.envName`), and sets
  `keys.folderMode` on the opened plan so its help shows the `:files` hint. The `y`/`Y` keys
  copy the response body / whole response pane (ANSI-stripped) to the clipboard, surfaced via
  the green ✓ `notice` line.

- **`internal/clipboard`** — `Copy(text)` writes to the system clipboard by shelling out to
  the platform tool (`pbcopy`/`clip`/`wl-copy`/`xclip`/`xsel`); no third-party dependency.
  `Available()` reports whether any tool is present.

- **`internal/config`** — persists the chosen theme to the OS user-config dir
  (`lazy-http/config.json`); best-effort, never fatal.

### Key behaviors worth knowing before editing

- **Variable lifecycle (`runner.Plan`):** `Vars` = env file + inline `@defs` + values captured
  from responses. `BaseVars` is the pristine env+inline snapshot. Expansion against `Vars`
  happens in `Plan.Expand()` at the moment a step runs, so a capture from step N is visible to
  step N+1. `Plan.Evaluate()` runs captures and assertions after a result arrives. The TUI
  calls these via its `*runner.Plan`; `ui.Model.onResult` is the wiring that invokes them.

- **`@reset` and reset semantics:** a successful step marked `@reset` calls `Plan.Reset()`,
  which clears all *other* results and drops captured vars back to `BaseVars` — mirroring a
  backend reset the request just performed. In the TUI, `ui.Model.resetState()` wraps it to
  also clear the cached bodies and stop the chain. `C` (clear all) and switching environment
  do the same reset.

- **"Run from here" chaining:** `runFrom` (−1 = inactive) drives the `a` key. `onResult`
  advances to the next step only while the current one's `Result.OK()` holds; the chain
  stops on the first failure or end of plan.

- **Idle-by-design rendering:** the spinner tick loop only runs while a step is in flight
  (`anyRunning()`); an untouched UI performs zero redraws. Display names (`names`) and
  highlighted bodies (`bodyView`) are cached and rebuilt only on load/result/reset, not per
  frame.

## Docs

`docs/http-format.md` is the authoritative spec for the `.http` syntax lazyhttp accepts;
`example.http` is a complete runnable tour. Keep both in sync with parser changes in
`internal/httpfile`.
