package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func schemaRoot(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	p := filepath.Join(root, "docs", "dsl", "widget.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func runSchema(t *testing.T, args ...string) (out, errOut string, err error) {
	t.Helper()
	t.Chdir(t.TempDir())
	errOut = captureStderr(t, func() {
		out = captureStdout(t, func() {
			err = cmdSchema(args)
		})
	})
	return out, errOut, err
}

// --root reads no config, and the working directory holds none: a
// command that quietly needed one would fail here rather than in a
// project gate the first time the config and the binary disagree.
func TestSchemaCommandCompilesFromRootAloneWithNoConfig(t *testing.T) {
	root := schemaRoot(t, "## #1 The file\n\n- **#2 A widget names itself.**\n  - key `name`: string, required\n")
	out, _, err := runSchema(t, "--root", root, "dsl:widget")
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	props, _ := s["properties"].(map[string]any)
	name, _ := props["name"].(map[string]any)
	if s["$schema"] != "https://json-schema.org/draft/2020-12/schema" || name["x-rule"] != "widget#2" {
		t.Errorf("schema = %s", out)
	}
}

func TestSchemaCommandRefusesAndPrintsEveryFinding(t *testing.T) {
	root := schemaRoot(t, "## #1 The file\n\n- key `a`: string\n- key `b.c`: string, optional\n")
	out, errOut, err := runSchema(t, "--root", root, "widget")
	if err == nil || !strings.Contains(err.Error(), "docs/dsl/widget.md was not compiled: 2 problem(s)") {
		t.Fatalf("err = %v", err)
	}
	if out != "" {
		t.Errorf("a refused compile printed a schema anyway:\n%s", out)
	}
	for _, want := range []string{"`a` is a named member and states neither", "`b.c` sits in `b`, which is not declared"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
}

func TestSchemaCommandNamesEveryPlaceItLooked(t *testing.T) {
	root := schemaRoot(t, "## #1 The file\n")
	_, _, err := runSchema(t, "--root", root, "nope")
	if err == nil || !strings.Contains(err.Error(), "no ported systems/nope.md, screens/nope.md or docs/dsl/nope.md") {
		t.Errorf("err = %v", err)
	}
}
