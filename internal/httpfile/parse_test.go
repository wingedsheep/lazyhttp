package httpfile

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wingedsheep/lazyhttp/internal/step"
)

const sample = `@host = https://api.test

### List
GET {{host}}/items
Accept: application/json

### Create
POST {{host}}/items
Content-Type: application/json

{
  "name": "x"
}

### Seed
# @shell
# @name Seed DB
echo "host is {{host}}"
`

func TestParse(t *testing.T) {
	steps := Parse(sample, Vars{})
	if len(steps) != 3 {
		t.Fatalf("want 3 steps, got %d", len(steps))
	}

	get := steps[0]
	if get.Method != "GET" || get.URL != "{{host}}/items" {
		t.Errorf("step 0 wrong: %+v", get)
	}
	if get.Name != "List" {
		t.Errorf("name from ### header: got %q", get.Name)
	}
	if get.Headers["Accept"] != "application/json" {
		t.Errorf("header not parsed: %+v", get.Headers)
	}

	post := steps[1]
	if post.Method != "POST" || post.Body == "" {
		t.Errorf("post body missing: %+v", post)
	}

	sh := steps[2]
	if sh.Kind != step.KindShell {
		t.Errorf("expected shell step, got kind %d", sh.Kind)
	}
	if sh.Name != "Seed DB" {
		t.Errorf("@name directive: got %q", sh.Name)
	}
	if sh.Body != `echo "host is {{host}}"` {
		t.Errorf("shell body should keep raw placeholder: %q", sh.Body)
	}
}

func TestVarsNotExpandedAtParse(t *testing.T) {
	// Placeholders are kept raw; expansion happens at execution time.
	steps := Parse("### x\nGET {{host}}/p\n", Vars{"host": "http://env"})
	if steps[0].URL != "{{host}}/p" {
		t.Errorf("URL should keep raw placeholder, got %q", steps[0].URL)
	}
}

func TestParseRequestLine(t *testing.T) {
	// Method defaults to GET, is upper-cased, and a bare URL (with or without a
	// trailing version token) is read as the URL rather than mistaken for a method.
	cases := []struct {
		line                string
		wantMethod, wantURL string
	}{
		{"GET /items", "GET", "/items"},
		{"post /items", "POST", "/items"},
		{"https://x.com/items", "GET", "https://x.com/items"},
		{"https://x.com/items HTTP/1.1", "GET", "https://x.com/items"},
		{"{{host}}/items HTTP/1.1", "GET", "{{host}}/items"},
		{"DELETE https://x.com/items HTTP/2", "DELETE", "https://x.com/items"},
	}
	for _, tc := range cases {
		steps := Parse("### r\n"+tc.line+"\n", Vars{})
		if len(steps) != 1 {
			t.Fatalf("%q: want 1 step, got %d", tc.line, len(steps))
		}
		if steps[0].Method != tc.wantMethod || steps[0].URL != tc.wantURL {
			t.Errorf("%q: got %s %q, want %s %q",
				tc.line, steps[0].Method, steps[0].URL, tc.wantMethod, tc.wantURL)
		}
	}
}

const grouped = `### List
# @group Posts
GET /posts

### Create
# @capture postId = json.id
POST /posts

{ "x": 1 }

### Echo
# @group Utilities
# @shell
echo hi

### Cleanup
# @group Posts
DELETE /posts/1
`

func TestGroupsAndCaptures(t *testing.T) {
	steps := Parse(grouped, Vars{})
	if len(steps) != 4 {
		t.Fatalf("want 4 steps, got %d", len(steps))
	}

	wantGroups := []string{"Posts", "Posts", "Utilities", "Posts"}
	for i, want := range wantGroups {
		if steps[i].Group != want {
			t.Errorf("step %d group: want %q, got %q", i, want, steps[i].Group)
		}
	}

	caps := steps[1].Captures
	if len(caps) != 1 || caps[0].Name != "postId" || caps[0].Expr != "json.id" {
		t.Errorf("capture not parsed: %+v", caps)
	}
}

func TestParseAssertions(t *testing.T) {
	src := `### Check
# @assert status == 201
# @assert header.Content-Type contains json
# @assert json.id exists
# @assert status in 200, 204
# @assert body not contains error
# @assert header.Location matches ^/orders/\d+$
POST /posts
`
	steps := Parse(src, Vars{})
	a := steps[0].Asserts
	if len(a) != 6 {
		t.Fatalf("want 6 assertions, got %d", len(a))
	}
	if a[0].Expr != "status" || a[0].Op != "==" || a[0].Want != "201" || a[0].Negated {
		t.Errorf("assert 0 wrong: %+v", a[0])
	}
	if a[1].Op != "contains" || a[1].Want != "json" {
		t.Errorf("assert 1 wrong: %+v", a[1])
	}
	if a[2].Op != "exists" || a[2].Want != "" {
		t.Errorf("assert 2 wrong: %+v", a[2])
	}
	// `in` keeps its list (and inner spaces) raw — splitting is capture.Check's job.
	if a[3].Op != "in" || a[3].Want != "200, 204" || a[3].Negated {
		t.Errorf("assert 3 wrong: %+v", a[3])
	}
	// a `not` prefix sets Negated and the operator is the token after it.
	if a[4].Expr != "body" || a[4].Op != "contains" || a[4].Want != "error" || !a[4].Negated {
		t.Errorf("assert 4 wrong: %+v", a[4])
	}
	// a regex want is preserved verbatim — no quote-stripping, no escaping.
	if a[5].Op != "matches" || a[5].Want != `^/orders/\d+$` {
		t.Errorf("assert 5 wrong: %+v", a[5])
	}
}

func TestParseReset(t *testing.T) {
	steps := Parse("### Clear\n# @reset\nDELETE /db\n", Vars{})
	if len(steps) != 1 || !steps[0].Reset {
		t.Fatalf("reset directive not parsed: %+v", steps)
	}
}

func TestParsePerRequestDirectives(t *testing.T) {
	steps := Parse("### Slow\n# @no-redirect\n# @timeout 5 s\nGET /slow\n", Vars{})
	if len(steps) != 1 {
		t.Fatalf("want 1 step, got %d", len(steps))
	}
	s := steps[0]
	if !s.NoRedirect {
		t.Errorf("NoRedirect = false, want true")
	}
	if s.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s", s.Timeout)
	}
}

func TestParseStream(t *testing.T) {
	cases := []struct {
		name        string
		directive   string
		wantStream  bool
		wantExtract string
		wantThrough string
	}{
		{"bare", "# @stream", true, "", ""},
		{"extract", "# @stream choices[0].delta.content", true, "choices[0].delta.content", ""},
		{"through", "# @stream-through jq -rj '.choices[0].delta.content // empty'", true, "", "jq -rj '.choices[0].delta.content // empty'"},
		{"none", "", false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "### S\n"
			if tc.directive != "" {
				src += tc.directive + "\n"
			}
			src += "GET /events\n"
			s := Parse(src, Vars{})[0]
			if s.Stream != tc.wantStream {
				t.Errorf("Stream = %v, want %v", s.Stream, tc.wantStream)
			}
			if s.StreamExtract != tc.wantExtract {
				t.Errorf("StreamExtract = %q, want %q", s.StreamExtract, tc.wantExtract)
			}
			if s.StreamThrough != tc.wantThrough {
				t.Errorf("StreamThrough = %q, want %q", s.StreamThrough, tc.wantThrough)
			}
		})
	}
}

func TestParseTimeout(t *testing.T) {
	cases := []struct {
		arg  string
		want time.Duration // 0 means "should be ignored"
	}{
		{"30 s", 30 * time.Second},
		{"500 ms", 500 * time.Millisecond},
		{"2 m", 2 * time.Minute},
		{"30s", 30 * time.Second},
		{"15", 15 * time.Second},          // bare number defaults to seconds
		{"1.5s", 1500 * time.Millisecond}, // fractional, via time.ParseDuration
		{"1h", time.Hour},                 // hours now supported
		{"5 h", 5 * time.Hour},            // space-separated hours too
		{"1h30m", 90 * time.Minute},       // compound duration
		{"abc", 0},                        // unparseable
		{"0 s", 0},                        // non-positive ignored
		{"-5s", 0},                        // negative ignored
		{"", 0},
	}
	for _, tc := range cases {
		t.Run(tc.arg, func(t *testing.T) {
			d, ok := parseTimeout(tc.arg)
			if tc.want == 0 {
				if ok {
					t.Errorf("parseTimeout(%q) = %v, want ignored", tc.arg, d)
				}
				return
			}
			if !ok || d != tc.want {
				t.Errorf("parseTimeout(%q) = %v (ok=%v), want %v", tc.arg, d, ok, tc.want)
			}
		})
	}
}

func TestParseBodyFromFile(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantFile string
		wantVars bool
	}{
		{"plain", "< ./body.json", "./body.json", false},
		{"expand", "<@ ./body.xml", "./body.xml", true},
		{"expand encoding", "<@latin1 ./body.txt", "./body.txt", true},
		{"no space", "<./body.json", "./body.json", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps := Parse("### Send\nPOST /items\nContent-Type: application/json\n\n"+tc.body+"\n", Vars{})
			if len(steps) != 1 {
				t.Fatalf("want 1 step, got %d", len(steps))
			}
			s := steps[0]
			if s.BodyFile != tc.wantFile {
				t.Errorf("BodyFile = %q, want %q", s.BodyFile, tc.wantFile)
			}
			if s.BodyFileVars != tc.wantVars {
				t.Errorf("BodyFileVars = %v, want %v", s.BodyFileVars, tc.wantVars)
			}
			if s.Body != "" {
				t.Errorf("inline Body should be empty for a file ref, got %q", s.Body)
			}
		})
	}
}

// TestParseScriptBodiesIgnored verifies response-handler (`>`) and pre-request
// (`< {% … %}`) scripts are not mistaken for a body or a body-file reference.
func TestParseScriptBodiesIgnored(t *testing.T) {
	respHandler := Parse("### A\nGET /x\n\n> {% client.test(); %}\n", Vars{})[0]
	if respHandler.Body != "" || respHandler.BodyFile != "" {
		t.Errorf("response handler should be ignored: %+v", respHandler)
	}
	preReq := Parse("### B\nGET /x\n\n< {% request.variables.set('a', 1) %}\n", Vars{})[0]
	if preReq.Body != "" || preReq.BodyFile != "" {
		t.Errorf("pre-request script should be ignored: %+v", preReq)
	}
}

// TestParseCRLF feeds a Windows-authored plan (\r\n line endings) and checks
// that no trailing \r leaks onto URLs, header values, inline @defs, captured-var
// names, or @assert right-hand sides — and that a blank "\r" line still ends the
// header section instead of becoming part of the body.
func TestParseCRLF(t *testing.T) {
	src := "@host = https://api.test\r\n" +
		"\r\n" +
		"### Create\r\n" +
		"# @capture postId = json.id\r\n" +
		"# @assert status == 201\r\n" +
		"POST {{host}}/items\r\n" +
		"Content-Type: application/json\r\n" +
		"\r\n" +
		"{\"name\":\"x\"}\r\n"

	steps := Parse(src, Vars{})
	if len(steps) != 1 {
		t.Fatalf("want 1 step, got %d", len(steps))
	}
	s := steps[0]

	if s.URL != "{{host}}/items" {
		t.Errorf("URL carries \\r: %q", s.URL)
	}
	if got := s.Headers["Content-Type"]; got != "application/json" {
		t.Errorf("header value carries \\r: %q", got)
	}
	if s.Body != `{"name":"x"}` {
		t.Errorf("body wrong (blank \\r line should end headers): %q", s.Body)
	}
	if len(s.Captures) != 1 || s.Captures[0].Name != "postId" || s.Captures[0].Expr != "json.id" {
		t.Errorf("capture carries \\r: %+v", s.Captures)
	}
	if len(s.Asserts) != 1 || s.Asserts[0].Want != "201" {
		t.Errorf("assert RHS carries \\r: %+v", s.Asserts)
	}
}

// writeFile is a test helper that drops content at dir/name and fails the test
// on error.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// TestImportSplicesSteps checks that `# @import ./other.http` inlines the
// imported file's steps at the point of import, in order, and that the imported
// file's inline @defs land in the shared variable set.
func TestImportSplicesSteps(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "auth.http", "@token = secret\n\n### Login\nPOST /login\n")
	main := writeFile(t, dir, "main.http",
		"### Before\nGET /a\n\n"+
			"### Pull in auth\n# @import ./auth.http\n\n"+
			"### After\nGET /b\n")

	vars := Vars{}
	steps, err := ParseFile(main, vars)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	got := make([]string, len(steps))
	for i, s := range steps {
		got[i] = s.Name
	}
	want := []string{"Before", "Login", "After"}
	if len(got) != len(want) {
		t.Fatalf("want %v steps, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step %d: want %q, got %q", i, want[i], got[i])
		}
	}
	if vars["token"] != "secret" {
		t.Errorf("imported @def not merged: %q", vars["token"])
	}
}

// TestImportRelativeToImportingFile checks that a nested import resolves
// relative to the file that declares it, not the top-level plan.
func TestImportRelativeToImportingFile(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, sub, "leaf.http", "### Leaf\nGET /leaf\n")
	// mid.http lives in sub/ and imports leaf.http as a sibling.
	writeFile(t, sub, "mid.http", "### Mid\nGET /mid\n\n### inc\n# @import ./leaf.http\n")
	main := writeFile(t, dir, "main.http", "### Main\nGET /main\n\n### inc\n# @import ./sub/mid.http\n")

	steps, err := ParseFile(main, Vars{})
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	got := make([]string, len(steps))
	for i, s := range steps {
		got[i] = s.Name
	}
	want := []string{"Main", "Mid", "Leaf"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

// TestImportCycleErrors checks that a mutual import (a → b → a) fails with a
// cycle error rather than recursing forever.
func TestImportCycleErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.http", "### A\nGET /a\n\n### inc\n# @import ./b.http\n")
	b := writeFile(t, dir, "b.http", "### B\nGET /b\n\n### inc\n# @import ./a.http\n")

	if _, err := ParseFile(b, Vars{}); err == nil {
		t.Fatal("want cycle error, got nil")
	}
}

// TestImportMissingFileErrors checks that importing a file that does not exist
// surfaces the read error.
func TestImportMissingFileErrors(t *testing.T) {
	dir := t.TempDir()
	main := writeFile(t, dir, "main.http", "### inc\n# @import ./nope.http\n")
	if _, err := ParseFile(main, Vars{}); err == nil {
		t.Fatal("want error for missing import, got nil")
	}
}

// TestImportDiamondRunsTwice checks that importing the same file from two places
// is allowed (not flagged as a cycle) and contributes its steps each time.
func TestImportDiamondRunsTwice(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "common.http", "### Common\nGET /c\n")
	main := writeFile(t, dir, "main.http",
		"### one\n# @import ./common.http\n\n### two\n# @import ./common.http\n")

	steps, err := ParseFile(main, Vars{})
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("want 2 steps (common twice), got %d", len(steps))
	}
}
