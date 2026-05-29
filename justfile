# lazyhttp task runner — https://github.com/casey/just
# Run `just` with no arguments to list recipes.

_default:
    @just --list

# Build the binary to bin/lazyhttp (gitignored).
build:
    go build -o bin/lazyhttp .

# Run the full test suite.
test:
    go test ./...

# Vet the code.
vet:
    go vet ./...

# Open a plan in the TUI (defaults: example.http, env dev).
run plan="example.http" env="dev": build
    ./bin/lazyhttp --env {{env}} {{plan}}

# Start the local OAuth2 demo server (token + echo resource) on :9000.
demo-server:
    python3 scripts/oauth-demo-server.py

# Run example.oauth.http against the local demo server (start `just demo-server` first).
demo: build
    ./bin/lazyhttp --env local example.oauth.http
