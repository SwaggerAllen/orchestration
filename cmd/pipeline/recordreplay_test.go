package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/agent"
	"github.com/SwaggerAllen/orchestration/internal/changespec"
	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/plane"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/setup"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

// exportScript lifts the step that exports the record as it stood at
// the pass's start out of the design action, the way recordCommand
// lifts the diff extraction: read from the shipped file, so editing
// the action is what this test runs.
//
// Anchored on the step's name and sliced to the next step, because
// unlike the two pipelines above it this one is a multi-line `run:`
// block rather than a backslash-continued command, and walking back
// over continuations would lift one line of it.
func exportScript(t *testing.T, base string) string {
	t.Helper()
	body := repoFile(t, filepath.Join(".github", "actions", "agent-design", "action.yml"))
	i := strings.Index(body, "name: export the record as it stood at the pass's start")
	if i < 0 {
		t.Fatal("the design action no longer exports the record at the pass's start")
	}
	step := body[i:]
	if j := strings.Index(step[1:], "\n    - name:"); j >= 0 {
		step = step[:j+1]
	}
	k := strings.Index(step, "run: |")
	if k < 0 {
		t.Fatal("the export step has no run block")
	}
	var lines []string
	for _, l := range strings.Split(step[k+len("run: |"):], "\n") {
		lines = append(lines, strings.TrimPrefix(l, "        "))
	}
	script := strings.Join(lines, "\n")
	return strings.ReplaceAll(script, "${{ steps.branch.outputs.before }}", base)
}

// runShell runs a lifted action script in dir with RUNNER_TEMP set,
// which is the one runner variable the lifted steps read.
func runShell(t *testing.T, dir, runnerTemp, script string) string {
	t.Helper()
	c := exec.Command("bash", "-c", script)
	c.Dir = dir
	c.Env = append(os.Environ(), "RUNNER_TEMP="+runnerTemp)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("running the action's script: %v\n%s", err, out)
	}
	return string(out)
}

const portedCaps = "---\npaths:\n  - lib/app/caps/**\n---\n\n# caps\n\n## #1 Standing decisions\n\n- **#17 One Repo.** Stores own schemas and queries, never connections.\n- **#3 Empty is a real state.** It renders as such.\n\n## #2 Depends on\n\n- nothing.\n"

const capsReasons = "# caps — reasons\n\n## #17\nsince: ORC-22\n\nA shared connection is a second owner, and two owners drift.\n"

// portedRepo is a project whose one system doc is ported, with a design
// branch on which the pass has rewritten rule #17 against its reason and
// added a screen. before is the branch head the pass started from — what
// the action records as steps.branch.outputs.before.
func portedRepo(t *testing.T) (dir, before string) {
	t.Helper()
	dir = t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "systems/caps.md", portedCaps)
	write(t, dir, "systems/caps.reasons.md", capsReasons)
	write(t, dir, "README.md", "a project\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "the project, with one ported doc")
	git(t, dir, "checkout", "-qb", "design")
	before = git(t, dir, "rev-parse", "HEAD")
	write(t, dir, "systems/caps.md", strings.Replace(portedCaps, "never connections.", "or connections, whichever is convenient.", 1))
	write(t, dir, "screens/cap.md", "---\nfiles:\n  - lib/app_web/components/cap.ex\n---\n\n# cap\n\n## #1 Two states\n\nThe cap copy is a decision.\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "the pass's work")
	return dir, before
}

// The export takes each of systems/ and screens/ that the start commit
// has and skips the one it lacks. The guard exists for the second half:
// `git archive` fails outright on a pathspec matching nothing, and a
// project's first system doc has no screens/ to export — so without it
// the step dies, the review is assembled with no base tree, and every
// touched rule reads as new.
func TestTheRecordExportTakesWhatTheStartCommitHasAndSkipsWhatItLacks(t *testing.T) {
	dir, before := portedRepo(t)
	if out, err := exec.Command("git", "-C", dir, "cat-file", "-e", before+":screens").CombinedOutput(); err == nil {
		t.Fatalf("the start commit has screens/, so the guard's case was never built: %s", out)
	}
	runnerTemp := t.TempDir()
	out := runShell(t, dir, runnerTemp, exportScript(t, before))
	base := filepath.Join(runnerTemp, "pipeline", "record-base")
	for _, want := range []string{"systems/caps.md", "systems/caps.reasons.md"} {
		raw, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(want)))
		if err != nil {
			t.Fatalf("the export did not land %s: %v\n%s", want, err, out)
		}
		if want == "systems/caps.md" && !strings.Contains(string(raw), "never connections.") {
			t.Errorf("the exported doc is the branch's, not the start commit's:\n%s", raw)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "screens")); err == nil {
		t.Error("the export invented a screens/ the start commit did not have")
	}
	if !strings.Contains(out, "record-base/systems/caps.reasons.md") {
		t.Errorf("the step does not list what it exported:\n%s", out)
	}
}

// replayWorld is the design job's world with the model swapped out: a
// memory tracker and host, the real claim, the real action scripts
// over a real git repo, the real prompt assembly, and a canned verdict
// where the reviewer's run would be.
type replayWorld struct {
	tr     *tracker.Memory
	h      *host.Memory
	cfg    *config.Config
	p      *plane.Plane
	dir    string
	base   string
	tmp    string
	res    *agent.ClaimResult
	key    string
	id     string
	out    string
	err    string
	prompt string
}

func replay(t *testing.T) *replayWorld {
	t.Helper()
	ctx := context.Background()
	dir, before := portedRepo(t)
	cfg := config.Sample()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "pipeline.config.json", string(raw))
	cfg, err = config.Load(filepath.Join(dir, "pipeline.config.json"))
	if err != nil {
		t.Fatal(err)
	}
	tr := tracker.NewMemory()
	h := host.NewMemory()
	if _, err := setup.Run(ctx, tr, cfg, false); err != nil {
		t.Fatal(err)
	}
	p := plane.New(tr, cfg).WithHost(h)
	states, err := tr.ListStates(ctx, cfg.Tracker.TeamID)
	if err != nil {
		t.Fatal(err)
	}
	var ready string
	for _, s := range states {
		if s.Name == cfg.StateName(protocol.ReadyForDesign) {
			ready = s.ID
		}
	}
	issue, err := tr.CreateIssue(ctx, tracker.NewIssue{TeamID: cfg.Tracker.TeamID, ProjectID: cfg.Tracker.ProjectID,
		Title: "Caps may share a connection", Description: "Let stores share one connection when convenient.", StateID: ready})
	if err != nil {
		t.Fatal(err)
	}
	res, err := agent.ClaimDesign(ctx, p, issue.Key, "run_1", "https://gh/run/1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w := &replayWorld{tr: tr, h: h, cfg: cfg, p: p, dir: dir, base: before, tmp: t.TempDir(), res: res, key: issue.Key, id: issue.ID}
	pipe := filepath.Join(w.tmp, "pipeline")
	if err := os.MkdirAll(filepath.Join(pipe, "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The three action scripts, lifted from the shipped file and run as
	// the job runs them: what the pass wrote, its record diff, and the
	// record as it stood when it started.
	changed := runShell(t, dir, w.tmp, authoredCommand(t, before))
	// The replay reconstructs a design pass from a real merge, and those
	// merges predate the spec file. FinishDesign holds every artifacts
	// pass to writing one, so a replay without it stands for a pass the
	// protocol no longer allows — and would fail on the spec rather than
	// on the record review this exercises.
	if err := os.WriteFile(filepath.Join(dir, changespec.Name),
		[]byte(changespec.Header(w.res.TicketKey, w.res.Title)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed = strings.TrimRight(changed, "\n") + "\n" + changespec.Name + "\n"
	write(t, pipe, "changed.txt", changed)
	write(t, pipe, "record-diff.patch", runShell(t, dir, w.tmp, recordCommand(t, before)))
	runShell(t, dir, w.tmp, exportScript(t, before))
	ghOut := filepath.Join(w.tmp, "github-output")
	t.Setenv("GITHUB_OUTPUT", ghOut)
	// The shipped reviewer prompt, resolved before standing somewhere
	// else as the real run does.
	tpl, err := filepath.Abs(filepath.Join("..", "..", "prompts", "record-review.md"))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	w.out = captureStdout(t, func() {
		err = cmdAgentRecordReview([]string{
			"--config", filepath.Join(dir, "pipeline.config.json"),
			"--diff", filepath.Join(pipe, "record-diff.patch"),
			"--changed-files", filepath.Join(pipe, "changed.txt"),
			"--out", filepath.Join(pipe, "review"),
			"--prompt-template", tpl,
			"--outcome-path", filepath.Join(pipe, "review", "review.json"),
			"--base-tree", filepath.Join(pipe, "record-base"),
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(ghOut); !strings.Contains(string(got), "reviewable=true") {
		t.Fatalf("the job would skip the model run: GITHUB_OUTPUT = %q", got)
	}
	prompt, err := os.ReadFile(filepath.Join(pipe, "review", "prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	w.prompt = string(prompt)
	return w
}

// finish is the job's finish step with the reviewer's file where the
// model would have left it: the same resolve, the same FinishDesign.
func (w *replayWorld) finish(t *testing.T, reviewJSON string) {
	t.Helper()
	pipe := filepath.Join(w.tmp, "pipeline")
	if reviewJSON != "" {
		write(t, pipe, filepath.Join("review", "review.json"), reviewJSON)
	}
	review, reviewErr, err := resolveRecordReview(filepath.Join(pipe, "review", "review.json"), filepath.Join(pipe, "review", "run-error.txt"))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := readPathList(filepath.Join(pipe, "changed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	o := &agent.DesignOutcome{Outcome: "artifacts", Screens: []string{"cap"}, Systems: []string{"caps"}, Summary: "Stores may share a connection when convenient."}
	if err := agent.FinishDesign(context.Background(), w.p, w.h, w.res, o, "", w.base, changed, changed, review, reviewErr); err != nil {
		t.Fatal(err)
	}
}

func (w *replayWorld) state(t *testing.T) protocol.State {
	t.Helper()
	ctx := context.Background()
	issues, err := w.tr.ListIssues(ctx, w.cfg.Tracker.TeamID, w.cfg.Tracker.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	states, _ := w.tr.ListStates(ctx, w.cfg.Tracker.TeamID)
	for _, s := range states {
		if s.ID == issues[0].StateID {
			for _, st := range protocol.AllStates {
				if w.cfg.StateName(st) == s.Name {
					return st
				}
			}
		}
	}
	t.Fatalf("no state for %q", issues[0].StateID)
	return ""
}

func (w *replayWorld) reviewMarkers(t *testing.T) []marker.Marker {
	t.Helper()
	issues, err := w.tr.ListIssues(context.Background(), w.cfg.Tracker.TeamID, w.cfg.Tracker.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	var out []marker.Marker
	for _, c := range issues[0].Comments {
		m, ok, err := marker.Parse(c.Body)
		if err == nil && ok && m.Kind == marker.RecordReview {
			m.Fields["_body"] = c.Body
			out = append(out, m)
		}
	}
	return out
}

// The whole design job with the model swapped for a canned verdict: the
// real scripts over a real repo, the real prompt, the real finish. Ring
// 2 posts the marker itself and so proves the sweep's rule; this proves
// the harness between the pass's commit and that marker — the seam no
// unit test crosses, and the one a rehearsal against the dummy cannot
// read into.
func TestADesignJobReplayedWithACannedDeclineRoutesOnTheReview(t *testing.T) {
	w := replay(t)
	// The reviewer is handed the base rule and entry for the one id the
	// pass touched, and nothing about the ticket.
	for _, want := range []string{
		"### caps#17 — systems/caps.md",
		"- **#17 One Repo.** Stores own schemas and queries, never connections.",
		"A shared connection is a second owner, and two owners drift.",
		"+- **#17 One Repo.** Stores own schemas and queries, or connections, whichever is convenient.",
		"+## #1 Two states",                   // the screen doc is the record too: a probe narrowing the extraction to systems/ passed until this line
		"- screens/cap.md\n- systems/caps.md", // changed.txt is sort -u'd by the action
	} {
		if !strings.Contains(w.prompt, want) {
			t.Errorf("the review prompt does not carry %q:\n%s", want, w.prompt)
		}
	}
	for _, absent := range []string{"caps#3", "caps#1 —", "Caps may share a connection", "Let stores share"} {
		if strings.Contains(w.prompt, absent) {
			t.Errorf("the review prompt carries %q — an untouched rule or the ticket", absent)
		}
	}
	w.finish(t, `{"verdict":"decline","findings":[{"file":"systems/caps.md","quote":"or connections, whichever is convenient","why":"the entry records that a shared connection is a second owner","kind":"contradiction","id":"caps#17"}],"summary":"The rule was rewritten against its reason and the entry was not amended."}`)
	if got := w.state(t); got != protocol.ReadyForRedesign {
		t.Errorf("state = %q, want ready_for_redesign: a decline takes the author's own decline route, into the queue whose name says so", got)
	}
	if len(w.h.PRs) != 0 {
		t.Errorf("a declined pass opened a PR: %+v", w.h.PRs)
	}
	ms := w.reviewMarkers(t)
	if len(ms) != 1 || ms[0].Fields["verdict"] != "decline" {
		t.Fatalf("record-review markers = %+v, want one decline", ms)
	}
	for _, want := range []string{"contradicts `caps#17`", "or connections, whichever is convenient", "pipeline reasons"} {
		if !strings.Contains(ms[0].Fields["_body"], want) {
			t.Errorf("the decline on the ticket does not carry %q:\n%s", want, ms[0].Fields["_body"])
		}
	}
}

func TestADesignJobReplayedWithACannedPassProceedsToReview(t *testing.T) {
	w := replay(t)
	w.finish(t, `{"verdict":"pass","findings":[],"summary":"The entry and the new rule read as consistent."}`)
	if got := w.state(t); got != protocol.DesignReview {
		t.Errorf("state = %q, want design_review", got)
	}
	if len(w.h.PRs) != 1 {
		t.Errorf("want one PR, got %+v", w.h.PRs)
	}
	ms := w.reviewMarkers(t)
	if len(ms) != 1 || ms[0].Fields["verdict"] != "pass" {
		t.Errorf("record-review markers = %+v, want one pass", ms)
	}
}

// A reviewer that never wrote its file — a run that died, or one that
// ended without writing — is said on the ticket and the pass proceeds:
// the review is a proposal about the record, never a gate on the work.
func TestADesignJobReplayedWithNoVerdictProceedsAndSaysSo(t *testing.T) {
	w := replay(t)
	write(t, filepath.Join(w.tmp, "pipeline"), filepath.Join("review", "run-error.txt"), "the model run died: exit 1\n")
	w.finish(t, "")
	if got := w.state(t); got != protocol.DesignReview {
		t.Errorf("state = %q, want design_review", got)
	}
	if ms := w.reviewMarkers(t); len(ms) != 0 {
		t.Errorf("a dead reviewer posted a verdict: %+v", ms)
	}
	issues, _ := w.tr.ListIssues(context.Background(), w.cfg.Tracker.TeamID, w.cfg.Tracker.ProjectID)
	said := false
	for _, c := range issues[0].Comments {
		if strings.Contains(c.Body, "the model run died") {
			said = true
		}
	}
	if !said {
		t.Error("the dead reviewer is not said on the ticket")
	}
}
