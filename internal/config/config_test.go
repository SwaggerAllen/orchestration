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
		// A provider nothing implements has to be refused here, because
		// nothing downstream refuses it: deployPort's switch has no
		// default, so an unknown value yields a nil port, which is how
		// "this project has no deploy detection" is spelled. The project
		// would validate, sweep, and leave every Merged ticket riding to
		// the deploy timeout.
		{"deploy.provider unknown", func(m map[string]any) { m["deploy"].(map[string]any)["provider"] = "heroku" }, `deploy.provider: "heroku" is not a provider`},
		// Both messages are built from config.DeployProviders, so a value
		// added to the var alone would validate while the error text went
		// on denying it existed. Assert the text names the real set.
		{"deploy.provider names the set", func(m map[string]any) { m["deploy"].(map[string]any)["provider"] = "heroku" }, "one of render, digitalocean, github"},
		{"deploy.endpoint", func(m map[string]any) { m["deploy"].(map[string]any)["endpoint"] = "" }, "deploy.endpoint: missing"},
		{"deploy.timeout", func(m map[string]any) { delete(m["deploy"].(map[string]any), "timeout") }, "deploy.timeout: missing"},
		{"staleClaimGrace", func(m map[string]any) { delete(m, "staleClaimGrace") }, "staleClaimGrace: missing"},
		{"preview.pagesProject", func(m map[string]any) { m["preview"].(map[string]any)["pagesProject"] = "" }, "preview.pagesProject: missing"},
		{"preview.buildCommand", func(m map[string]any) { delete(m["preview"].(map[string]any), "buildCommand") }, "preview.buildCommand: missing"},
		{"preview.outputDir", func(m map[string]any) { delete(m["preview"].(map[string]any), "outputDir") }, "preview.outputDir: missing"},
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

// The first of the three merges that retire the Pages preview (PLAN §6.2,
// C4). This is the one that gives the project a side to go first from: it
// has to validate against a config that still carries the block *and* one
// that has dropped it, or each side alone is invalid and the project's
// every command fails until both have landed.
func TestValidateAcceptsAConfigWithNoPreviewBlock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(m map[string]any)
	}{
		{"key absent", func(m map[string]any) { delete(m, "preview") }},
		{"block empty", func(m map[string]any) { m["preview"] = map[string]any{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sampleAsMap(t)
			tc.mutate(m)
			c, err := Load(writeConfig(t, m))
			if err != nil {
				t.Fatalf("a project with no preview wired must validate: %v", err)
			}
			if c.Preview != (Preview{}) {
				t.Errorf("preview = %+v, want the zero value", c.Preview)
			}
		})
	}
}

// Optional as a block, required as a whole. Half a Pages preview publishes
// nowhere while reading as configured, and the three keys are only
// meaningful together — so dropping one is a typo, not a project opting
// out. The rows in TestValidateNamesEveryMissingField above are exactly
// that case and still fail; this asserts the two behaviours are different,
// which is the whole content of "optional as a block".
func TestValidateStillRejectsAHalfWiredPreview(t *testing.T) {
	m := sampleAsMap(t)
	m["preview"] = map[string]any{"pagesProject": "sample-storybook"}
	_, err := Load(writeConfig(t, m))
	if err == nil {
		t.Fatal("a preview block naming a project but nothing to publish must fail")
	}
	for _, want := range []string{"preview.buildCommand: missing", "preview.outputDir: missing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
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

func TestActorsAllowSharedAuthorControlplane(t *testing.T) {
	// The solo-workspace exception: a personal API key IS the author.
	m := sampleAsMap(t)
	m["actors"].(map[string]any)["author"] = []any{"usr_solo"}
	m["actors"].(map[string]any)["controlplane"] = []any{"usr_solo"}
	if _, err := Load(writeConfig(t, m)); err != nil {
		t.Errorf("shared author/controlplane id must validate, got %v", err)
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

// Every project written before this field existed omits it, and the
// pipeline still has to look for the file — a config that predates a
// feature should get the feature's default, not opt out of it.
func TestLoadDefaultsTheNonAsksPath(t *testing.T) {
	m := sampleAsMap(t)
	delete(m, "nonAsksPath")
	c, err := Load(writeConfig(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if c.NonAsksPath != DefaultNonAsksPath {
		t.Errorf("nonAsksPath = %q, want the default %q", c.NonAsksPath, DefaultNonAsksPath)
	}
}

// An explicit path wins — a project that keeps the file somewhere else
// has said so on purpose.
func TestLoadKeepsAnExplicitNonAsksPath(t *testing.T) {
	m := sampleAsMap(t)
	m["nonAsksPath"] = "docs/wont-do.md"
	c, err := Load(writeConfig(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if c.NonAsksPath != "docs/wont-do.md" {
		t.Errorf("nonAsksPath = %q, want %q", c.NonAsksPath, "docs/wont-do.md")
	}
}

// An explicit "" is opting out, and it must survive — a defaulting rule
// that cannot tell "absent" from "deliberately empty" gives the project
// no way to turn the feature off.
func TestLoadTreatsAnEmptyNonAsksPathAsOptingOut(t *testing.T) {
	m := sampleAsMap(t)
	m["nonAsksPath"] = ""
	c, err := Load(writeConfig(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if c.NonAsksPath != "" {
		t.Errorf("nonAsksPath = %q, want it left empty", c.NonAsksPath)
	}
}

// The agent commands run with the working directory inside the pipeline
// checkout, not the project (`go -C .pipeline run ...`). A path read
// relative to cwd finds nothing on every real run, so paths resolve
// against the config file's own directory.
func TestInRootResolvesAgainstTheConfigsDirectory(t *testing.T) {
	path := writeConfig(t, sampleAsMap(t))
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(path), "non-asks.md")
	if got := c.InRoot("non-asks.md"); got != want {
		t.Errorf("InRoot() = %q, want %q", got, want)
	}
	if got := c.InRoot("/etc/passwd"); got != "/etc/passwd" {
		t.Errorf("InRoot(absolute) = %q, want it untouched", got)
	}
}

// The table Catapult actually ships, verbatim from ORC-144. This is the
// test the field exists for: the config edit is already written on
// catapult PR #84, and if it merges against a binary that cannot decode
// it, Load fails and every pipeline command stops — not just the
// citation check. Both value forms and a filename-shaped key are here
// because all three are in that file.
func TestLoadAcceptsTheShippedCitationShorthands(t *testing.T) {
	m := sampleAsMap(t)
	m["citationShorthands"] = map[string]any{
		"v5":            map[string]any{"path": "docs/v5-design-decisions.md"},
		"v4":            map[string]any{"path": "seed-docs/catapult-spec-v4.md"},
		"conventions":   map[string]any{"path": "docs/conventions.md"},
		"Conventions":   map[string]any{"path": "docs/conventions.md"},
		"dsl-syntax.md": map[string]any{"path": "docs/dsl-syntax.md"},
		"DESIGN":        map[string]any{"unchecked": "orchestration's DESIGN.md — another repository"},
		"orchestration": map[string]any{"unchecked": "orchestration's own docs — another repository"},
		"AGPL":          map[string]any{"unchecked": "the AGPL-3.0 licence text, not a file in this tree"},
	}
	c, err := Load(writeConfig(t, m))
	if err != nil {
		t.Fatalf("Load rejected the shipped table: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate rejected the shipped table: %v", err)
	}
	if got := c.CitationShorthands["v5"].Path; got != "docs/v5-design-decisions.md" {
		t.Errorf("v5.path = %q", got)
	}
	if c.CitationShorthands["AGPL"].Unchecked == "" {
		t.Error("AGPL.unchecked did not survive the decode")
	}
}

// Nested unknown fields are rejected too — DisallowUnknownFields is a
// decoder setting, not a top-level one. So declaring only the outer map
// would not have been enough: a config using the unchecked form would
// still fail to load. Measured, because "the outer field is declared"
// reads like it settles the question and does not.
func TestAnUndeclaredFieldInsideAShorthandIsRejected(t *testing.T) {
	m := sampleAsMap(t)
	m["citationShorthands"] = map[string]any{
		"v5": map[string]any{"path": "docs/v5.md", "reason": "not a field"},
	}
	_, err := Load(writeConfig(t, m))
	if err == nil || !strings.Contains(err.Error(), "reason") {
		t.Errorf("want unknown-field error naming reason, got %v", err)
	}
}

// Absence is the state every project is in until it writes the table,
// and it must stay loadable and valid — otherwise declaring the field
// breaks every config that does not have it yet.
func TestCitationShorthandsAreOptional(t *testing.T) {
	c, err := Load(writeConfig(t, sampleAsMap(t)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("a config with no citationShorthands must validate: %v", err)
	}
	if c.CitationShorthands != nil {
		t.Errorf("want nil map, got %v", c.CitationShorthands)
	}
}

func TestAShorthandNeedsEitherPathOrUnchecked(t *testing.T) {
	m := sampleAsMap(t)
	m["citationShorthands"] = map[string]any{"v5": map[string]any{}}
	// Load validates, so the complaint arrives from there rather than
	// from a separate Validate call.
	_, err := Load(writeConfig(t, m))
	if err == nil || !strings.Contains(err.Error(), "citationShorthands.v5") {
		t.Errorf("want a complaint naming the entry, got %v", err)
	}
}

func TestAShorthandRejectsBothPathAndUnchecked(t *testing.T) {
	m := sampleAsMap(t)
	m["citationShorthands"] = map[string]any{
		"v5": map[string]any{"path": "docs/v5.md", "unchecked": "and also not checkable"},
	}
	_, err := Load(writeConfig(t, m))
	if err == nil || !strings.Contains(err.Error(), "contradict") {
		t.Errorf("want a contradiction complaint, got %v", err)
	}
}
