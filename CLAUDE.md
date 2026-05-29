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
./bin/lazyhttp example.http      # run against the bundled example plan
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
  untouched on purpose so the user can see what failed to resolve.

- **`internal/exec`** — executes a step as a `tea.Cmd` that runs off the UI thread and
  delivers a `ResultMsg{Index, Result}` when done (so the TUI never blocks). `http.go` does
  the HTTP request (shared 30s-timeout client, pretty-prints JSON bodies); `shell.go` runs
  the body via `$SHELL -c`, capturing combined stdout+stderr and the exit code.

- **`internal/capture`** — evaluates capture/assert expressions against a `Result`:
  `status`, `body`, `header.Name`, or a JSON path (`json.a.b[0].c`, `$.a`, or bare `a.b`).
  `Eval` is used for both captures (store into vars) and assertion left-hand sides.

- **`internal/auth`** — the OAuth2 helper. Parses the env file's IntelliJ-style
  `Security.Auth` blocks into `Config`s (via `httpfile.LoadAuth`); a `Resolver` then
  substitutes `{{$auth.token("id")}}` / `{{$auth.idToken("id")}}` placeholders in a
  step's URL/headers/body by fetching a token from the config's Token URL. A
  thread-safe `Cache` reuses tokens until `expires_in` lapses. Only the Client
  Credentials and Password grants are implemented (no browser round-trip). Wired in
  off the UI thread through the `exec.AuthResolver` hook in `runHTTP`; `ui/model.go`
  expands config values on the UI thread (so secrets resolve without racing the
  request goroutine) then hands a `Resolver` to `exec.Run`.

- **`internal/ui`** — the Bubble Tea root. `model.go` is the heart: it owns the parsed
  steps, per-step results, the cursor, and the **`vars` map** that accumulates captured
  values as steps run. `view.go` renders the two-pane layout, `styles.go`/`json.go` handle
  theming and JSON highlighting, `keys.go` defines the keymap.

- **`internal/config`** — persists the chosen theme to the OS user-config dir
  (`lazy-http/config.json`); best-effort, never fatal.

### Key behaviors worth knowing before editing

- **Variable lifecycle (`ui/model.go`):** `vars` = env file + inline `@defs` + values captured
  from responses. `baseVars` is the pristine env+inline snapshot. Expansion against `vars`
  happens in `expand()` at the moment a step runs, so a capture from step N is visible to
  step N+1. `evaluate()` runs captures and assertions after a result arrives.

- **`@reset` and reset semantics:** a successful step marked `@reset` calls `resetState()`,
  which clears all *other* results and drops captured vars back to `baseVars` — mirroring a
  backend reset the request just performed. `C` (clear all) and switching environment do the
  same reset.

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
