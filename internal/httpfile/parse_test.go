package httpfile

import (
	"testing"

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
POST /posts
`
	steps := Parse(src, Vars{})
	a := steps[0].Asserts
	if len(a) != 3 {
		t.Fatalf("want 3 assertions, got %d", len(a))
	}
	if a[0].Expr != "status" || a[0].Op != "==" || a[0].Want != "201" {
		t.Errorf("assert 0 wrong: %+v", a[0])
	}
	if a[1].Op != "contains" || a[1].Want != "json" {
		t.Errorf("assert 1 wrong: %+v", a[1])
	}
	if a[2].Op != "exists" || a[2].Want != "" {
		t.Errorf("assert 2 wrong: %+v", a[2])
	}
}

func TestParseReset(t *testing.T) {
	steps := Parse("### Clear\n# @reset\nDELETE /db\n", Vars{})
	if len(steps) != 1 || !steps[0].Reset {
		t.Fatalf("reset directive not parsed: %+v", steps)
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
