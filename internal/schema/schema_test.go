package schema

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/reasons"
)

func index(t *testing.T, body string) reasons.Index {
	t.Helper()
	return reasons.Index{Dir: "docs/dsl", Name: "widget", Path: "docs/dsl/widget.md", Doc: reasons.ParseDoc(body)}
}

// The golden schema pins the whole mapping — named members and their
// presence, `*`, `[]`, nested `[]`, a shape used alone and in a union,
// quoted values, `any`, a heading rule, and `when` — byte for byte,
// because the output is a contract a project's gate consumes and a
// change to it is a change to every project's check.
// UPDATE_GOLDEN=1 rewrites it; read the diff before committing one.
func TestTheGoldenDocCompilesToItsPinnedSchema(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "declared.md"))
	if err != nil {
		t.Fatal(err)
	}
	s, problems := Compile(index(t, string(body)))
	if len(problems) > 0 {
		t.Fatalf("problems = %q", problems)
	}
	got, err := Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "declared.schema.json")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("schema differs from %s:\n%s", golden, got)
	}
}

// Byte-stable across runs: Go randomises map iteration, and every map in
// the compiler is ranged over, so a single unsorted range would show up
// here as an intermittent diff rather than as a failure on the day.
func TestCompilingTwiceGivesTheSameBytes(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "declared.md"))
	if err != nil {
		t.Fatal(err)
	}
	var first []byte
	for i := 0; i < 20; i++ {
		s, _ := Compile(index(t, string(body)))
		b, err := Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = b
		} else if !bytes.Equal(first, b) {
			t.Fatalf("run %d differs from run 0", i)
		}
	}
}

func TestADocWithAFindingIsNotCompiled(t *testing.T) {
	s, problems := Compile(index(t, "## #1 Rule\n\n- key `a`: string\n"))
	if s != nil || len(problems) != 1 || !strings.Contains(problems[0], "states neither required nor optional") {
		t.Errorf("schema = %v problems = %q", s, problems)
	}
}

// An empty schema accepts every file, so a gate comparing its code
// against one would pass having checked nothing.
func TestADocDeclaringNothingIsNotCompiled(t *testing.T) {
	s, problems := Compile(index(t, "## #1 Rule\n\nProse only.\n"))
	if s != nil || len(problems) != 1 || !strings.Contains(problems[0], "declares no attributes") {
		t.Errorf("schema = %v problems = %q", s, problems)
	}
}
