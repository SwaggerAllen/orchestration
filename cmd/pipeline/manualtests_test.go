package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/config"
)

// manualTestTree writes a project with two systems, one screen and three
// manual tests, and returns its root.
func manualTestTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"systems", "screens", "tests/manual"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Sample()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"pipeline.config.json":           string(raw),
		"systems/engine.md":              "---\npaths:\n  - lib/engine/**\n---\n\n# Engine\n",
		"systems/delivery.md":            "---\npaths:\n  - lib/delivery/**\n---\n\n# Delivery\n",
		"screens/board.md":               "---\nfiles:\n  - screens/board/**\n---\n\n# Board\n",
		"tests/manual/queue-drains.md":   "---\ncovers:\n  - system:engine\n---\n\n# drains\n",
		"tests/manual/board-renders.md":  "---\ncovers:\n  - screen:board\n---\n\n# renders\n",
		"tests/manual/dispatch-lands.md": "---\ncovers:\n  - system:engine\n  - system:delivery\n---\n\n# lands\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func changedFile(t *testing.T, root string, paths ...string) string {
	t.Helper()
	p := filepath.Join(root, "changed.txt")
	if err := os.WriteFile(p, []byte(strings.Join(paths, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func runManualTests(t *testing.T, args ...string) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := cmdManualTests(args)
	_ = w.Close()
	os.Stdout = old
	out, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(out), runErr
}

// The tier is explicit, never inferred from whether a file was passed. A
// flag that means "everything" when --changed-files is missing runs the
// whole set the first time somebody forgets to write it, and a judge pass
// spends the model subscription.
//
// This is the test the predicate needed: it shipped as
// `*all == (*changedPath == "")`, which rejects both legal invocations
// and accepts neither illegal one — and the package's other tests never
// saw it, because they exercise the selection rather than the CLI. One
// run against a real tree found it.
func TestManualTestsRequiresExactlyOneTier(t *testing.T) {
	root := manualTestTree(t)
	cfg := filepath.Join(root, "pipeline.config.json")
	changed := changedFile(t, root, "lib/engine/foo.ex")

	for _, tc := range []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"changed only", []string{"--config", cfg, "--changed-files", changed}, false},
		{"all only", []string{"--config", cfg, "--all"}, false},
		{"both", []string{"--config", cfg, "--all", "--changed-files", changed}, true},
		{"neither", []string{"--config", cfg}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runManualTests(t, tc.args...)
			if tc.wantErr && err == nil {
				t.Fatal("want an error naming the two flags, got none")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("legal invocation rejected: %v", err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "exactly one") {
				t.Errorf("error %q does not name the choice", err)
			}
		})
	}
}

func TestManualTestsSelectsBySeam(t *testing.T) {
	root := manualTestTree(t)
	cfg := filepath.Join(root, "pipeline.config.json")

	for _, tc := range []struct {
		name    string
		changed []string
		want    []string
	}{
		{"engine", []string{"lib/engine/foo.ex"}, []string{"tests/manual/dispatch-lands.md", "tests/manual/queue-drains.md"}},
		{"screen", []string{"screens/board/x.ex"}, []string{"tests/manual/board-renders.md"}},
		{"neither", []string{"README.md"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runManualTests(t, "--config", cfg, "--changed-files", changedFile(t, root, tc.changed...))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				if l != "" {
					got = append(got, l)
				}
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("selected %v, want %v", got, tc.want)
			}
		})
	}
}

// Repo-relative, whatever --root was. The caller feeds these to a sparse
// checkout and names check runs after them, and a check run's name has to
// match across commits for rule 8 to compare two verdicts for one test.
func TestManualTestsPrintsRepoRelativePaths(t *testing.T) {
	root := manualTestTree(t)
	out, err := runManualTests(t, "--config", filepath.Join(root, "pipeline.config.json"), "--all")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, root) {
		t.Errorf("output carries the runner's absolute path:\n%s", out)
	}
	if !strings.Contains(out, "tests/manual/queue-drains.md") {
		t.Errorf("output = %q", out)
	}
}

// An empty selection is a legal matrix that runs no jobs, which is the
// right shape for a diff touching no covered seam — not an error, and not
// a matrix with one empty entry.
func TestManualTestsEmptyJSONMatrixIsValid(t *testing.T) {
	root := manualTestTree(t)
	out, err := runManualTests(t, "--config", filepath.Join(root, "pipeline.config.json"),
		"--changed-files", changedFile(t, root, "README.md"), "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Include []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"include"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, out)
	}
	if len(m.Include) != 0 {
		t.Errorf("include = %+v, want empty", m.Include)
	}
}

func TestManualTestsJSONCarriesIDAndPath(t *testing.T) {
	root := manualTestTree(t)
	out, err := runManualTests(t, "--config", filepath.Join(root, "pipeline.config.json"),
		"--changed-files", changedFile(t, root, "screens/board/x.ex"), "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Include []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"include"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Include) != 1 || m.Include[0].ID != "board-renders" ||
		m.Include[0].Path != "tests/manual/board-renders.md" {
		t.Errorf("include = %+v", m.Include)
	}
}

func TestManualTestsRejectsAnUnknownFormat(t *testing.T) {
	root := manualTestTree(t)
	_, err := runManualTests(t, "--config", filepath.Join(root, "pipeline.config.json"), "--all", "--format", "yaml")
	if err == nil || !strings.Contains(err.Error(), "unknown --format") {
		t.Errorf("err = %v", err)
	}
}
