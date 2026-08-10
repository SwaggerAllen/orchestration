package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, m map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pipeline.config.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func sampleAsMap(t *testing.T) map[string]any {
	t.Helper()
	raw, err := json.Marshal(Sample())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSampleIsValid(t *testing.T) {
	if err := Sample().Validate(); err != nil {
		t.Fatalf("Sample() must validate: %v", err)
	}
}

// The example file is documentation, and documentation that can drift from
// the fixture the tests trust is documentation that lies. One canonical
// example, enforced.
func TestExampleFileMatchesSample(t *testing.T) {
	loaded, err := Load(filepath.Join("..", "..", "examples", "pipeline.config.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(Sample())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("examples/pipeline.config.json does not match config.Sample():\n got: %s\nwant: %s", got, want)
	}
}

func TestLoadValid(t *testing.T) {
	c, err := Load(writeConfig(t, sampleAsMap(t)))
	if err != nil {
		t.Fatal(err)
	}
	if c.StateName("designing") != "Designing" {
		t.Errorf("StateName(designing) = %q", c.StateName("designing"))
	}
	if c.StaleClaimGrace.Duration().Minutes() != 20 {
		t.Errorf("staleClaimGrace = %v", c.StaleClaimGrace.Duration())
	}
}

// Every required field's absence must produce a message naming that field —
// the M0 gate. Mutations delete one field (or one state mapping) at a time.
func TestValidateNamesEveryMissingField(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(m map[string]any)
		wantMsg string
	}{
		{"version", func(m map[string]any) { delete(m, "version") }, "version:"},
		{"teamId", func(m map[string]any) { m["tracker"].(map[string]any)["teamId"] = "" }, "tracker.teamId: missing"},
		{"projectId", func(m map[string]any) { m["tracker"].(map[string]any)["projectId"] = "" }, "tracker.projectId: missing"},
		{"states", func(m map[string]any) { delete(m, "states") }, "states: missing"},
		{"one state", func(m map[string]any) { delete(m["states"].(map[string]any), "reconciling") }, "states.reconciling: missing"},
		{"designOwnedPaths", func(m map[string]any) { delete(m, "designOwnedPaths") }, "designOwnedPaths: missing"},
		{"qualityGates", func(m map[string]any) { delete(m, "qualityGates") }, "qualityGates: missing"},
		{"deploy.provider", func(m map[string]any) { delete(m["deploy"].(map[string]any), "provider") }, "deploy.provider: missing"},
		{"deploy.endpoint", func(m map[string]any) { m["deploy"].(map[string]any)["endpoint"] = "" }, "deploy.endpoint: missing"},
		{"deploy.timeout", func(m map[string]any) { delete(m["deploy"].(map[string]any), "timeout") }, "deploy.timeout: missing"},
		{"staleClaimGrace", func(m map[string]any) { delete(m, "staleClaimGrace") }, "staleClaimGrace: missing"},
		{"preview.pagesProject", func(m map[string]any) { m["preview"].(map[string]any)["pagesProject"] = "" }, "preview.pagesProject: missing"},
		{"preview.buildCommand", func(m map[string]any) { delete(m["preview"].(map[string]any), "buildCommand") }, "preview.buildCommand: missing"},
		{"preview.outputDir", func(m map[string]any) { delete(m["preview"].(map[string]any), "outputDir") }, "preview.outputDir: missing"},
		{"milestoneNaming", func(m map[string]any) { delete(m, "milestoneNaming") }, "milestoneNaming: missing"},
		{"actors", func(m map[string]any) { delete(m, "actors") }, "actors: missing"},
		{"actors.author", func(m map[string]any) { delete(m["actors"].(map[string]any), "author") }, "actors.author: missing"},
		{"actors.controlplane", func(m map[string]any) { delete(m["actors"].(map[string]any), "controlplane") }, "actors.controlplane: missing"},
		{"agents", func(m map[string]any) { delete(m, "agents") }, "agents: missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := sampleAsMap(t)
			tc.mutate(m)
			_, err := Load(writeConfig(t, m))
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not name the field (want substring %q)", err, tc.wantMsg)
			}
		})
	}
}

func TestValidateRejectsDuplicateTrackerNames(t *testing.T) {
	m := sampleAsMap(t)
	m["states"].(map[string]any)["reworking"] = "In progress"
	_, err := Load(writeConfig(t, m))
	if err == nil || !strings.Contains(err.Error(), "already used") {
		t.Errorf("want duplicate-name error, got %v", err)
	}
}

func TestValidateRejectsUnknownState(t *testing.T) {
	m := sampleAsMap(t)
	m["states"].(map[string]any)["needs_review"] = "Needs review"
	_, err := Load(writeConfig(t, m))
	if err == nil || !strings.Contains(err.Error(), "not a protocol state") {
		t.Errorf("want unknown-state error, got %v", err)
	}
}

func TestActorsRejectDoubleAssignment(t *testing.T) {
	m := sampleAsMap(t)
	m["actors"].(map[string]any)["dev"] = []any{"usr_author"}
	_, err := Load(writeConfig(t, m))
	if err == nil || !strings.Contains(err.Error(), "already assigned") {
		t.Errorf("want double-assignment error, got %v", err)
	}
}

func TestActorsRejectUnknownRole(t *testing.T) {
	m := sampleAsMap(t)
	m["actors"].(map[string]any)["janitor"] = []any{"usr_j"}
	_, err := Load(writeConfig(t, m))
	if err == nil || !strings.Contains(err.Error(), "not a pipeline role") {
		t.Errorf("want unknown-role error, got %v", err)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	m := sampleAsMap(t)
	m["staleClaimGraze"] = "20m" // typo of a real field
	_, err := Load(writeConfig(t, m))
	if err == nil || !strings.Contains(err.Error(), "staleClaimGraze") {
		t.Errorf("want unknown-field error, got %v", err)
	}
}

func TestDurationRejectsBareNumbers(t *testing.T) {
	m := sampleAsMap(t)
	m["staleClaimGrace"] = 20
	_, err := Load(writeConfig(t, m))
	if err == nil || !strings.Contains(err.Error(), "duration string") {
		t.Errorf("want duration-string error, got %v", err)
	}
}
