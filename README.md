# lazyhttp

A terminal UI for running `.http` test plans step by step — like a `lazygit`/`k9s`
for your HTTP requests. Open any `.http` file (the same format used by the IntelliJ
HTTP Client and the VS Code REST Client), step through requests, capture values,
and assert on responses.

![lazyhttp running the bundled example plan](.github/assets/screenshot.png)

## Install

Pick whichever fits the machine — all three give you a `lazyhttp` on your `PATH`.

### Homebrew (macOS / Linux — no Go needed)

```sh
brew install wingedsheep/tap/lazyhttp
```

Upgrade later with `brew upgrade lazyhttp`.

### curl one-liner (macOS / Linux — no Go, no Homebrew)

Downloads the latest prebuilt binary from GitHub Releases:

```sh
curl -fsSL https://raw.githubusercontent.com/wingedsheep/lazyhttp/main/install.sh | sh
```

It installs to `/usr/local/bin` if writable, otherwise `~/.local/bin`. Set a
custom location with `LAZYHTTP_INSTALL_DIR=/somewhere`.

### Scoop (Windows — no Go needed)

```powershell
scoop bucket add wingedsheep https://github.com/wingedsheep/scoop-bucket
scoop install lazyhttp
```

Or grab `lazyhttp_windows_<arch>.zip` from the
[latest release](https://github.com/wingedsheep/lazyhttp/releases/latest), unzip
it, and put `lazyhttp.exe` on your `PATH`. See the
[Windows notes](docs/http-format.md#windows-notes) for the default `@shell`
interpreter and CRLF handling.

### go install (any OS with Go 1.24+)

```sh
go install github.com/wingedsheep/lazyhttp@latest
```

This drops `lazyhttp` in your Go bin dir — make sure it's on your `PATH`:

```sh
# Add to ~/.zshrc (or ~/.bashrc) if `lazyhttp` isn't found after install:
export PATH="$PATH:$(go env GOPATH)/bin"
```

> **Don't have Go?** On macOS: `brew install go`. Or with mise: `mise use -g go@1.24`.

## Usage

Point it at any `.http` file:

```sh
lazyhttp example.http
lazyhttp --env dev example.http        # pick an environment from http-client.env.json
lazyhttp --theme dracula example.http  # set a colour theme (cycle in-app with `t`)
```

### Environments

If a `http-client.env.json` sits next to your `.http` file, `--env NAME` selects
a named environment and its values fill in `{{vars}}`:

```json
{
  "dev":     { "api": "https://dummyjson.com", "bin": "https://httpbin.org", "user": "emilys", "pass": "emilyspass" },
  "staging": { "api": "https://dummyjson.com", "bin": "https://httpbin.org", "user": "emilys", "pass": "emilyspass" }
}
```

### Keys

| Key            | Action                  |
| -------------- | ----------------------- |
| `↑/k` `↓/j`    | move                    |
| `g` / `G`      | first / last step       |
| `^u` / `^d`    | half-page up / down     |
| `enter` / `e`  | run the selected step   |
| `a`            | run from here onward    |
| `r`            | reload the file         |
| `c` / `C`      | clear result / clear all|
| `i`            | toggle request details  |
| `/`            | filter steps            |
| `t`            | cycle colour theme      |
| `E`            | switch environment      |
| `?`            | full help               |
| `q` / `^c`     | quit                    |

## Writing `.http` files

The full `.http` syntax lazyhttp accepts — steps, the `# @name` / `# @group` /
`# @capture` / `# @assert` / `# @shell` / `# @reset` / `# @import` directives,
capture and assertion expressions, and `{{variable}}` resolution — is documented in
**[docs/http-format.md](docs/http-format.md)**. See [`example.http`](example.http)
for a complete, runnable tour.

## Updating

- Homebrew: `brew upgrade lazyhttp`
- curl: re-run the install one-liner
- Go: `go install github.com/wingedsheep/lazyhttp@latest`

## Building from source

```sh
git clone https://github.com/wingedsheep/lazyhttp.git
cd lazyhttp
go build -o bin/lazyhttp .
./bin/lazyhttp example.http
```
