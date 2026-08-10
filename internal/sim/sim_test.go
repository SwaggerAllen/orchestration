package sim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/config"
)

func TestEmptyScenarioPasses(t *testing.T) {
	sc, err := Load(filepath.Join("testdata", "empty.json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), sc, config.Sample())
	if err != nil {
		t.Fatal(err)
	}
	if res.Scenario != "empty" || res.StepsRun != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestUnknownStepKindFailsLoudly(t *testing.T) {
	sc := &Scenario{Name: "future", Steps: []Step{{Kind: "sweep"}}}
	_, err := Run(context.Background(), sc, config.Sample())
	if err == nil || !strings.Contains(err.Error(), "no step kinds") {
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
