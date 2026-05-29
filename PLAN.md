# lazy-http

A beautiful terminal UI for running HTTP test plans **step by step** — read the
same `.http` files you already run in IntelliJ, execute each request manually,
and inspect the response in a side panel. Navigate the plan with `k9s`-style
keys. Optionally mix in shell steps for setup/teardown and assertions.

> Think `lazygit`/`k9s`, but for an ordered list of HTTP requests.

---

## 1. Goals

| # | Goal                                                                  | Priority     |
|---|-----------------------------------------------------------------------|--------------|
| 1 | Parse IntelliJ-compatible `.http` files into an ordered list of steps | Must         |
| 2 | Execute each step manually and show the response on the right         | Must         |
| 3 | `k9s`-style navigation: `j`/`k`, arrows, `g`/`G`, fast scroll         | Must         |
| 4 | Scroll the response body independently when it's large                | Must         |
| 5 | Run shell steps inline in the same file                               | Nice-to-have |
| 6 | Elegant, idiomatic, readable Go                                       | Must         |

### Non-goals (for v1)

- Test assertions / scripting (IntelliJ's `> {% client.test(...) %}` JS hooks).
  We'll *parse and skip* them, and revisit later.
- Concurrent / load testing — this is a **manual, deliberate** runner.
- Editing requests inside the TUI. Edit the `.http` file in your editor;
  `r` reloads it.

---

## 2. Stack

| Concern        | Choice                                                    | Why                                                                                |
|----------------|-----------------------------------------------------------|------------------------------------------------------------------------------------|
| Language       | **Go 1.24** (pinned via `mise.toml`)                      | Same lineage as `lazygit`/`lazydocker`/`k9s`; single static binary                 |
| TUI runtime    | [`bubbletea`](https://github.com/charmbracelet/bubbletea) | Elm architecture (`Model`/`Update`/`View`) maps 1:1 onto "steps + cursor + result" |
| Styling/layout | [`lipgloss`](https://github.com/charmbracelet/lipgloss)   | Declarative styles + the two-pane split                                            |
| Widgets        | [`bubbles`](https://github.com/charmbracelet/bubbles)     | `viewport` (scrollable result), `spinner` (in-flight), `help`                      |
| HTTP           | `net/http` (stdlib)                                       | No dependency needed                                                               |
| Shell          | `os/exec` (stdlib)                                        | Same                                                                               |

Version management is handled by **`mise`** (`mise.toml` pins Go 1.24), so the
build is reproducible for anyone who clones the repo.

---

## 3. The `.http` format we support

Standard IntelliJ / VS Code REST Client syntax:

```http
### A request is named by the comment line above it
GET https://api.example.com/users
Accept: application/json

###
POST https://api.example.com/users
Content-Type: application/json

{
  "name": "Ada"
}
```

Supported constructs in v1:

- `###` separators (each block = one step). The trailing text on the `###`
  line, or a `# @name` directive, becomes the step's display name.
- Request line: `METHOD URL [HTTP/version]`. Method defaults to `GET`.
- Headers: `Key: Value` lines until a blank line.
- Body: everything after the blank line, up to the next `###`.
- Variables: `{{host}}` substitution, with values from:
    1. An adjacent `http-client.env.json` (the IntelliJ convention), selected by
       `--env <name>`, **and/or**
    2. In-file `@host = https://...` definitions, **and/or**
    3. Values captured from earlier responses (see below).
  Placeholders are expanded at **execution time**, not parse time, so captured
  values can flow into later steps.
- `> file.json` external-body references and `<> response-handler` lines are
  parsed and ignored (logged as "unsupported") in v1.

### Response-capture variables

A step can pull values out of its response into named variables that later steps
reference as `{{name}}`. Captures are declared with `# @capture name = expr`
comment directives, so the file stays IntelliJ-valid.

```http
### Create a post
# @capture postId = json.id
# @capture loc    = header.Location
POST {{host}}/posts
Content-Type: application/json

{ "title": "hi" }

### Fetch it back using the captured id
GET {{host}}/posts/{{postId}}
```

Supported capture expressions:

| Expression      | Resolves to                                            |
|-----------------|--------------------------------------------------------|
| `status`        | the HTTP status code (or shell exit code)              |
| `header.Name`   | a response header value                                |
| `body`          | the entire response body                               |
| `json.a.b[0].c` | a path into the JSON body (`$.` / bare path also work) |

Captured values are shown in a **CAPTURED** section under the response, and are
reset on reload (`r`).

### Test assertions

A step can assert on its response with `# @assert <expr> <op> <want>` directives.
The left-hand expression uses the same syntax as captures; the operator compares
it against the expected value.

```http
### Get a single post
# @assert status == 200
# @assert json.id == 1
# @assert header.Content-Type contains json
# @assert json.title exists
GET {{host}}/posts/1
```

| Operator   | Passes when…                          |
|------------|---------------------------------------|
| `==`       | the value equals `want`               |
| `!=`       | the value differs from `want`         |
| `contains` | the value contains `want` (substring) |
| `exists`   | the expression resolves at all        |

After a step runs, its assertions are evaluated and shown in an **ASSERTIONS**
section (✓/✗ per check, with the actual value on failure). A step whose request
succeeded but has a failing assertion is marked failed (red `✗` in the list), and
a `run-from-here` chain stops there. The title bar shows an aggregate `✓ N ✗ M`
badge across every step that has run.

### Resetting plan state

When you're testing against a backend that has a "clear database" (or similar)
endpoint, mark that step with `# @reset`. When it runs **successfully**, lazy-http
returns the whole plan to a clean slate: every other step's result reverts to
*pending* and captured variables are dropped (re-seeded from the env + inline
defaults). The reset step keeps its own result, and is flagged with a `⟲` marker
in the list.

```http
### Clear the test database
# @reset
# @assert status == 204
DELETE {{host}}/admin/test-data
```

You can also reset manually: `c` clears the selected step's result, and `C`
clears every result and drops captures without running anything.

### Grouping steps

Steps can be organized into named sections with `# @group Name`. A group applies
to the step it's on and every following step until the next `@group`, and renders
as a non-selectable heading in the list (navigation still moves step-to-step).

```http
### List posts
# @group Posts
GET {{host}}/posts

### Create a post          (still in "Posts")
POST {{host}}/posts
```

### Shell steps (kept in the same file)

To keep a plan in **one IntelliJ-runnable file**, shell steps are expressed as a
`###` block carrying a `# @shell` directive. IntelliJ treats the whole block as
a comment + an unreachable request, so the file stays valid there; lazy-http
recognizes the directive and runs the body in `$SHELL`.

```http
### Seed the database
# @shell
# @name Seed DB
psql "$DATABASE_URL" -f ./seed.sql

### Now the API call that depends on it
GET {{host}}/users/1
```

Rules:

- A block is a shell step iff it carries a `# @shell` directive.
- The body (lines after the directives) is the script, run via `sh -c`.
- `{{var}}` substitution applies to shell bodies too, so plans share config.

---

## 4. Architecture

```
main.go            // flag parsing, load file, start the Bubble Tea program
internal/
  httpfile/
    parse.go       // .http text  -> []Step (raw, unexpanded)
    vars.go        // variable resolution (env json + in-file @defs) + Expand
  step/
    step.go        // Step (incl. Group, Captures), Kind, Result types
  capture/
    capture.go     // evaluate # @capture expressions against a Result
  exec/
    http.go        // runHTTP(step)  -> tea.Cmd emitting ResultMsg
    shell.go       // runShell(step) -> tea.Cmd emitting ResultMsg
  ui/
    model.go       // the Bubble Tea Model: state, expansion, capture flow
    view.go        // lipgloss layout: grouped list | result pane + footer
    keys.go        // key bindings (k9s-style) + help text
    styles.go      // lipgloss styles / theme
```

Placeholders stay raw in `Step`; the `Model` owns the variable map and calls
`vars.Expand` at execution time (and for the live request preview), layering in
values captured from earlier responses.

### Data model

```go
type StepKind int
const ( KindHTTP StepKind = iota; KindShell )

type Step struct {
    Name    string
    Kind    StepKind
    Method  string            // HTTP only
    URL     string            // HTTP only
    Headers map[string]string // HTTP only
    Body    string            // HTTP body or shell script
    Raw     string            // original text, for the detail view
}

type Status int
const ( Pending Status = iota; Running; Done; Failed )

type Result struct {
    Status     Status
    StatusCode int           // HTTP
    ExitCode   int           // Shell
    Header     http.Header
    Body       string        // response body or combined stdout+stderr
    Duration   time.Duration
    Err        error
}
```

`Model` holds `steps []Step`, `results []Result` (parallel slice), `cursor int`,
the result `viewport.Model`, focus (list vs. result), and the source file path
for reloads. Execution is a `tea.Cmd` so the UI never blocks; completion arrives
as a `resultMsg{index, Result}`.

---

## 5. Layout & interaction

```
┌─ lazy-http ─ example.http ──────────────────────────────────────────────┐
│ STEPS                       │ RESPONSE                                    │
│ ──────────────────────────  │ ─────────────────────────────────────────  │
│ ● 1  GET   /health      200 │ POST /users  →  201 Created   (142ms)       │
│ ▶ 2  POST  /users       201 │                                             │
│   3  GET   /users/1     ··· │ content-type: application/json              │
│   4  ⌘ Seed DB           0  │ location: /users/42                          │
│   5  DELETE /users/1    ··· │                                             │
│                             │ {                                           │
│                             │   "id": 42,                                 │
│                             │   "name": "Ada"                             │
│                             │ }                                           │
│                             │                                             │
├─────────────────────────────┴─────────────────────────────────────────── │
│ ↑/k up · ↓/j down · enter run · r reload · tab focus · / scroll · q quit  │
└────────────────────────────────────────────────────────────────────────── ┘
```

- **Left pane** = the plan. Status glyph + index + method + path + status code.
  Colour: pending grey, running amber (spinner), 2xx green, 4xx/5xx/error red.
- **Right pane** = result for the selected step: by default just the response
  output (status summary + scrollable body, JSON syntax-highlighted), followed by
  any **ASSERTIONS** and **CAPTURED** sections. Press `i` to also show the request
  preview (method/URL/headers/body) and the response headers.
- `⌘` glyph marks shell steps; `⟲` marks a step that resets plan state.
- The mouse wheel scrolls within the TUI (the list, or the response body when
  it's focused) — it never falls through to the terminal's scrollback.

### Keys (k9s-flavoured)

| Key                                   | Action                                           |
|---------------------------------------|--------------------------------------------------|
| `j` / `↓`, `k` / `↑`                  | Move cursor down / up                            |
| `g` / `G`                             | Jump to first / last step                        |
| `ctrl+d` / `ctrl+u`                   | Half-page jump through the list (fast scroll)    |
| `enter` / `e`                         | Execute the selected step                        |
| `a`                                   | Execute all steps from the cursor down, in order |
| `tab`                                 | Toggle focus between list and result pane        |
| `J`/`K`, PgUp/PgDn, or wheel (result) | Scroll the response body                         |
| `i`                                   | Toggle the request preview on the right          |
| `r`                                   | Reload the `.http` file from disk                |
| `c`                                   | Clear the selected step's result                 |
| `C`                                   | Clear all results and drop captured variables    |
| `?`                                   | Toggle full help                                 |
| `q` / `ctrl+c`                        | Quit                                             |

---

## 6. Execution semantics

- Running a step sets its status to `Running` and fires a `tea.Cmd`.
- HTTP: build `*http.Request` from method/url/headers/body, substitute `{{vars}}`,
  send with a client that has a sane timeout, capture status/headers/body/duration.
- Shell: `exec.Command(shell, "-c", script)`, capture combined output + exit code.
  Steps inherit the current environment plus resolved `{{vars}}` as env.
- `a` (run-all-from-here) chains commands: each `resultMsg` triggers the next
  step until the end or the first failure (configurable later).
- After a step finishes, its `# @capture` expressions are evaluated against the
  response and stored in the variable map, so later steps (and their live
  preview) expand with those values.

---

## 7. Build & run

```bash
mise install                       # provisions Go 1.24 (one time)
go build -o bin/lazy-http .
./bin/lazy-http example.http
./bin/lazy-http --env staging example.http   # uses http-client.env.json
```

---

## 8. Roadmap (post-v1)

1. Save responses to disk; copy body/headers/captured values to clipboard.
2. A plan-file index screen (pick from multiple `.http` files in a dir, k9s-style).
3. Search/filter steps by name, method, or group; collapse/expand groups.
4. Richer assertions (numeric comparisons, regex match, JSON-schema checks).
5. Highlighting for non-JSON bodies (XML/HTML).

---

## 9. Milestones

- **M1 — Skeleton:** module + deps, parse example file, render the two panes
  read-only with working navigation. *(done)*
- **M2 — Execute:** run HTTP steps, show results, colour by status. *(done)*
- **M3 — Shell + run-all:** `# @shell` steps and `a`. *(done)*
- **M4 — Captures + groups:** `# @capture` response variables, `# @group`
  sections, execution-time expansion. *(done)*
- **M5 — Polish:** reload, help overlay, theme, env-file support. *(done)*
- **M6 — Assertions + highlighting:** `# @assert` checks, pass/fail summary,
  JSON syntax highlighting, response output minimal by default. *(done)*
- **M7 — UX & performance pass:** *(done)*
  - *Snappiness:* the spinner only animates while a step is running, so an idle
    UI does zero redraws; response bodies are syntax-highlighted once and cached;
    step names are pre-expanded so list navigation does no per-frame work.
  - *Visuals:* Catppuccin Mocha/Latte adaptive palette, colour-coded HTTP method
    badges, right-aligned status codes in the list, a solid selection highlight,
    a full-width status bar (logo · path · env · position · ✓/✗ badge), and a
    richer response summary (`METHOD → 200 OK · 142ms`).
  - *Polish:* a scroll indicator (`↑↓ %`) on overflowing response bodies, styled
    empty/pending/load-error states, tree connectors (`├`/`╰`) nesting grouped
    steps, and `/` to filter steps by method/name/group (live, Esc clears).
- **M8 — Roadmap items** as above.
