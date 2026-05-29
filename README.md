# lazy-http

A terminal UI for running `.http` test plans step by step — like a `lazygit`/`k9s`
for your HTTP requests. Open any `.http` file (the same format used by the IntelliJ
HTTP Client and the VS Code REST Client), step through requests, capture values,
and assert on responses.

## Install

You need [Go](https://go.dev/dl/) 1.24+ on the machine. Then, one command:

```sh
go install github.com/wingedsheep/lazyhttp@latest
```

This builds the latest release and drops a `lazyhttp` binary in your Go bin
directory. Make sure that directory is on your `PATH`:

```sh
# Add to ~/.zshrc (or ~/.bashrc) if `lazyhttp` isn't found after install:
export PATH="$PATH:$(go env GOPATH)/bin"
```

Then open a new terminal (or `source` your shell rc) and you're set:

```sh
lazyhttp --help
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
  "dev":     { "api": "https://api.restful-api.dev", "token": "s3cr3t-demo-token" },
  "staging": { "api": "https://api.restful-api.dev", "token": "s3cr3t-staging-token" }
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

## Updating

Re-run the install command to pull the newest version:

```sh
go install github.com/wingedsheep/lazyhttp@latest
```

## Building from source

```sh
git clone https://github.com/wingedsheep/lazyhttp.git
cd lazyhttp
go build -o bin/lazyhttp .
./bin/lazyhttp example.http
```
