# Windows support

lazyhttp builds and ships for macOS and Linux only today. This document is the
strategy for adding Windows as a first-class target. The gap is **narrow and
mostly mechanical** — the TUI core (Bubble Tea), the HTTP/parse/capture engine
(pure stdlib), and config persistence already work cross-platform — so this is an
additive change, not a rewrite.

Ordering is by value-per-effort. Phases are independently shippable; each leaves
the tree building green on all three platforms.

> **Status (2026-05-29):** Phases 1–3 are implemented in the codebase. Phase 1
> additionally honors `$SHELL` on Windows so Git Bash / MSYS2 / Cygwin sessions
> run POSIX `@shell` bodies through bash. Phase 3's Scoop bucket and Windows CI
> are wired up but need a tagged release (and the `SCOOP_BUCKET_TOKEN` secret +
> `wingedsheep/scoop-bucket` repo) to validate end-to-end. Phase 4 (terminal /
> path polish) remains open pending real Windows usage.

## What already works on Windows

- **TUI core.** Bubble Tea / Lip Gloss support the Windows Terminal, ConHost, and
  modern PowerShell. No platform assumptions in `internal/ui`.
- **HTTP + parse + capture.** `internal/exec/http.go`, `internal/httpfile`,
  `internal/capture` are pure `net/http` + `encoding/json` + string handling.
- **Config persistence.** `internal/config/config.go` uses `os.UserConfigDir()`,
  which already resolves to `%AppData%` on Windows (`config.go:20`).

## Where the platform-specific code lives

- **Shell steps** — `internal/exec/shell.go:22-27`: hardcodes `$SHELL -c <body>`
  with a `/bin/sh` fallback. `$SHELL` is unset on Windows and `cmd.exe`/PowerShell
  don't accept `-c`. This is the one runtime feature that is actually broken on
  Windows; `@shell` steps fail to spawn.
- **Release matrix** — `.goreleaser.yaml:15-17`: `goos` lists only `darwin` and
  `linux`. No Windows binaries are produced.
- **Distribution** — `.goreleaser.yaml` ships a Homebrew cask + the `install.sh`
  curl one-liner. Neither serves Windows users.
- **Parser line handling** — `internal/httpfile/parse.go`: splits files on `\n`.
  Windows-authored `.http` files (and `http-client.env.json`) carry `\r\n`; a
  trailing `\r` riding along on header values, URLs, directive args, or env JSON is
  the classic silent footgun.
- **Body-from-file paths** — `< ./body` / `<@ ./file` resolution: confirm backslash
  separators and drive-letter paths resolve relative to the plan directory.

---

## Phase 1 — OS-aware shell steps

**Value:** high (without it, `@shell` is broken on Windows). **Effort:** small.

### Changes

1. **Split the runner by build tag.** Replace the body of `runShell`'s command
   construction with a small `shellCommand(body string) *exec.Cmd` helper, defined
   twice:
   - `internal/exec/shell_unix.go` (`//go:build !windows`) — current behavior:
     `$SHELL` else `/bin/sh`, with `-c`.
   - `internal/exec/shell_windows.go` (`//go:build windows`) — prefer PowerShell
     (`powershell -NoProfile -Command <body>`), falling back to `cmd /c <body>`.
     Honor a `COMSPEC`/explicit override if set.
2. Keep the rest of `runShell` (timing, combined stdout+stderr capture, exit code)
   platform-neutral — only the `*exec.Cmd` construction differs.

### Caveats / tests

- Decide the documented default shell on Windows (PowerShell is the sane default;
  note it in `docs/http-format.md` so `@shell` bodies aren't written assuming
  POSIX `sh`). Shell bodies are inherently non-portable — say so.
- Add a smoke test guarded by `runtime.GOOS` (or a build-tagged test) that runs a
  trivial `echo` and asserts the captured output + exit code.

---

## Phase 2 — Normalize line endings in the parser

**Value:** high (silent corruption otherwise). **Effort:** small.

### Changes

1. **Strip `\r` once, centrally.** In `internal/httpfile/parse.go`, trim a trailing
   `\r` from each line as it's read (or normalize `\r\n`→`\n` on the whole input
   before splitting). Do the same for `http-client.env.json` consumption in
   `vars.go` if it isn't already going through `encoding/json` (JSON tolerates
   `\r\n`, so the risk is concentrated in the line-oriented `.http` parser).
2. Verify directive parsing (`applyDirective`), request-line parsing
   (`parseHTTP`), header splitting, and body-ref parsing (`parseBodyRef`) all see
   `\r`-free input.

### Caveats / tests

- Add a parser test feeding a `\r\n` fixture and asserting URLs, header values,
  captured-var names, and `@assert` right-hand sides have no trailing `\r`.
- Watch the blank-line-ends-headers boundary: a line of just `\r` must count as
  blank, or bodies get misattached.

---

## Phase 3 — Release + distribution

**Value:** high (this is what actually makes it installable). **Effort:** small,
but needs a release to validate end-to-end.

### Changes

1. **Build matrix.** Add `windows` to `goos` in `.goreleaser.yaml`. Keep
   `amd64` + `arm64`. `CGO_ENABLED=0` is already set, so cross-compiles cleanly.
2. **Archive format.** Add a per-OS archive override so Windows ships `.zip`
   (Unix stays `.tar.gz`); set `name_template` to keep
   `lazyhttp_windows_<arch>.zip` predictable for any future installer. The binary
   should be `lazyhttp.exe` (GoReleaser appends `.exe` automatically for Windows).
3. **Distribution channel.** Pick one to start:
   - **Scoop** manifest (GoReleaser `scoops:`) — closest analogue to the Homebrew
     cask; needs a bucket repo + token, mirroring the existing tap setup.
   - **winget** — broader reach, more process (manifest PR to
     microsoft/winget-pkgs); can follow later.
   Document a manual fallback: download the `.zip` from the latest release and put
   `lazyhttp.exe` on `PATH`.
4. **CI.** Ensure `go test ./...` and `go vet ./...` run on a `windows-latest`
   runner in the workflow so regressions are caught before tagging, not after.

### Caveats / tests

- The Homebrew cask's `xattr` quarantine hook is macOS-only and already guarded by
  `if OS.mac?` — leave it; Windows needs no equivalent.
- Smoke-test the produced `.exe` in Windows Terminal: colors, key handling
  (`Ctrl+C`, arrows, `Ctrl+U`/`Ctrl+D`), and mouse-wheel scrolling can differ from
  Unix ttys.

---

## Phase 4 — Polish (after it runs)

Capture so it isn't rediscovered:

- **Path display & resolution.** Audit any place that prints or joins paths for
  backslash correctness (body-from-file preview, error messages). Prefer
  `filepath` over hardcoded `/`.
- **Terminal capability differences.** Older ConHost lacks some ANSI/Unicode
  glyphs (tree connectors `├`/`╰`, status dots, spinner frames). Confirm the
  Catppuccin themes and glyphs render, or provide an ASCII fallback set.
- **Env-file & default-shell docs.** Add a short "Windows notes" section to
  `docs/http-format.md`: default `@shell` interpreter, CRLF tolerance, and install.

---

## Suggested sequencing

1. **Phase 1** — OS-aware shell steps (fixes the one broken runtime feature).
2. **Phase 2** — CRLF normalization (stops silent corruption of Windows-authored
   plans).
3. **Phase 3** — add `windows` to the build matrix + a Scoop manifest + Windows CI.
4. **Phase 4** — terminal/path polish once real Windows usage surfaces issues.

Phases 1–2 can land and be unit-tested on any platform via build tags / fixtures.
Phase 3 is the one that needs an actual tagged release (and ideally a Windows
machine or runner) to validate end-to-end.
