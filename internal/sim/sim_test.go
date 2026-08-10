package sim

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/config"
)

// TestScenarios runs the whole Ring-2 library. Every scenario also
// asserts, via the harness itself, that setup is idempotent and that each
// sweep converges.
func TestScenarios(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no scenarios found")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			sc, err := Load(f)
			if err != nil {
				t.Fatal(err)
			}
			res, err := Run(context.Background(), sc, config.Sample())
			if err != nil {
				t.Fatal(err)
			}
			if res.StepsRun != len(sc.Steps) {
				t.Errorf("ran %d of %d steps", res.StepsRun, len(sc.Steps))
			}
		})
	}
}

func TestUnknownStepKindFailsLoudly(t *testing.T) {
	sc := &Scenario{Name: "future", Steps: []json.RawMessage{
		json.RawMessage(`{"kind": "quantum-entangle"}`),
	}}
	_, err := Run(context.Background(), sc, config.Sample())
	if err == nil || !strings.Contains(err.Error(), "unknown step kind") {
		t.Errorf("want loud unknown-kind failure, got %v", err)
	}
}

func TestLoadRejectsNamelessScenario(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	if err := os.WriteFile(path, []byte(`{"steps":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("want error for nameless scenario")
	}
}
