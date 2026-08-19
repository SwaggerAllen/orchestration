package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/plane"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

type reconcileWorld struct {
	ctx context.Context
	tr  *tracker.Memory
	h   *host.Memory
	cfg *config.Config
	p   *plane.Plane
	id  string
	res *ClaimResult
}

func seedReconciling(t *testing.T) *reconcileWorld {
	t.Helper()
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "The argument.", protocol.Reconciling)
	h.PRs = []host.PR{{Number: 5, Branch: strings.ToLower(i.Key) + "-cap", HeadSHA: "sha5", Draft: false}}

	res, err := ClaimReconcile(ctx, p, i.Key, "run_90", "https://gh/90", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "reconcile" || res.PRNumber != 5 || res.Scope != "The argument." {
		t.Fatalf("claim = %+v", res)
	}
	return &reconcileWorld{ctx: ctx, tr: tr, h: h, cfg: cfg, p: p, id: i.ID, res: res}
}

func (w *reconcileWorld) check(t *testing.T, want protocol.State) {
	t.Helper()
	if got := issueState(t, w.tr, w.cfg, w.id); got != want {
		t.Errorf("state = %q, want %q", got, want)
	}
}

func TestReconcilePassMergesAndMarks(t *testing.T) {
	w := seedReconciling(t)
	v := &Verdict{Outcome: "pass", Report: "The tab named in prose has no storybook variation — flagged even on a pass."}
	if err := FinishReconcile(w.ctx, w.p, w.h, w.res, v); err != nil {
		t.Fatal(err)
	}
	w.check(t, protocol.Merged)
	if len(w.h.PRs) != 0 || w.h.Merged[5] == "" {
		t.Errorf("PR not merged: open=%v merged=%v", w.h.PRs, w.h.Merged)
	}
	issues, _ := w.tr.ListIssues(w.ctx, w.cfg.Tracker.TeamID, w.cfg.Tracker.ProjectID)
	foundMarker := false
	for _, c := range issues[0].Comments {
		if strings.Contains(c.Body, "[pipeline:v1:merged]") && strings.Contains(c.Body, "sha="+w.h.Merged[5]) {
			foundMarker = true
		}
	}
	if !foundMarker {
		t.Error("merged marker with the merge SHA missing — the post-deploy check has no left-hand side without it")
	}
}

func TestReconcileFailBounces(t *testing.T) {
	w := seedReconciling(t)
	v := &Verdict{Outcome: "fail", Report: "The cap_reached copy was paraphrased; the standing decision on wording did not land."}
	if err := FinishReconcile(w.ctx, w.p, w.h, w.res, v); err != nil {
		t.Fatal(err)
	}
	w.check(t, protocol.ReadyForRework)
	if len(w.h.Merged) != 0 {
		t.Error("fail must not merge")
	}
	issues, _ := w.tr.ListIssues(w.ctx, w.cfg.Tracker.TeamID, w.cfg.Tracker.ProjectID)
	last := issues[0].Comments[len(issues[0].Comments)-1]
	if !strings.Contains(last.Body, "[pipeline:v1:reconcile-bounce]") || !strings.Contains(last.Body, "paraphrased") {
		t.Errorf("newest comment must be the bounce with the report (it is the rework scope): %q", last.Body)
	}
}

func TestReconcileCannotTellMergesWithLabel(t *testing.T) {
	w := seedReconciling(t)
	v := &Verdict{Outcome: "cannot-tell", Report: "The argument names a reader flow the export cannot demonstrate."}
	if err := FinishReconcile(w.ctx, w.p, w.h, w.res, v); err != nil {
		t.Fatal(err)
	}
	w.check(t, protocol.Merged)
	if w.h.Merged[5] == "" {
		t.Error("cannot-tell must still merge (DESIGN 11)")
	}
	issues, _ := w.tr.ListIssues(w.ctx, w.cfg.Tracker.TeamID, w.cfg.Tracker.ProjectID)
	hasLabel := false
	for _, l := range issues[0].Labels {
		if l == "needs-review" {
			hasLabel = true
		}
	}
	if !hasLabel {
		t.Error("needs-review label missing — ambiguity must not resolve as pass")
	}
}

func TestClaimReconcileRequiresPR(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	i := seed(t, tr, cfg, "No PR", "d", protocol.Reconciling)
	if _, err := ClaimReconcile(ctx, p, i.Key, "r", "u", time.Now()); err == nil || !strings.Contains(err.Error(), "nothing to verify") {
		t.Errorf("want no-PR refusal, got %v", err)
	}
}

func TestLoadVerdictValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "v.json")
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := LoadVerdict(write(`{"outcome":"fail","report":""}`)); err == nil {
		t.Error("fail without report must be rejected — the report is the rework scope")
	}
	if _, err := LoadVerdict(write(`{"outcome":"shrug","report":"x"}`)); err == nil {
		t.Error("unknown outcome must be rejected — ambiguity never resolves as pass")
	}
	if v, err := LoadVerdict(write(`{"outcome":"pass","report":""}`)); err != nil || v.Outcome != "pass" {
		t.Errorf("pass without report should load, got %v %v", v, err)
	}
}

// The post-deploy check reads a deployment; on the dummy's stand-in
// platform something has to create one. That used to be a project
// workflow on `push: main`, which cannot fire for a merge made with
// GITHUB_TOKEN — so no deployment was ever recorded for an agent merge
// and the ticket sat in Merged until the deploy timeout moved it to
// Blocked. Reconcile does it now, and this is the test that says so.
func TestReconcileRecordsTheStandInDeployment(t *testing.T) {
	w := seedReconciling(t)
	w.cfg.Deploy.Provider = "github"
	w.cfg.Deploy.Endpoint = "production"

	if err := FinishReconcile(w.ctx, w.p, w.h, w.res, &Verdict{Outcome: "pass"}); err != nil {
		t.Fatal(err)
	}
	if len(w.h.Deployments) != 1 {
		t.Fatalf("deployments = %+v, want exactly the merge's", w.h.Deployments)
	}
	got := w.h.Deployments[0]
	if got.SHA != w.h.Merged[5] {
		t.Errorf("deployment sha = %q, want the merge commit %q — the ancestry check compares against this",
			got.SHA, w.h.Merged[5])
	}
	if got.Environment != "production" {
		t.Errorf("environment = %q, want the config's deploy.endpoint", got.Environment)
	}
}

// A real platform deploys itself and the sweep polls it. Recording one
// here would be the pipeline telling itself something shipped.
func TestReconcileRecordsNothingOnARealPlatform(t *testing.T) {
	w := seedReconciling(t)
	w.cfg.Deploy.Provider = "digitalocean"

	if err := FinishReconcile(w.ctx, w.p, w.h, w.res, &Verdict{Outcome: "pass"}); err != nil {
		t.Fatal(err)
	}
	if len(w.h.Deployments) != 0 {
		t.Errorf("deployments = %+v, want none — DigitalOcean rolls out on its own", w.h.Deployments)
	}
}

// A bounce does not merge, so there is nothing deployed to record.
func TestReconcileFailRecordsNoDeployment(t *testing.T) {
	w := seedReconciling(t)
	w.cfg.Deploy.Provider = "github"

	if err := FinishReconcile(w.ctx, w.p, w.h, w.res, &Verdict{Outcome: "fail", Report: "missing the second line"}); err != nil {
		t.Fatal(err)
	}
	if len(w.h.Deployments) != 0 {
		t.Errorf("deployments = %+v, want none on a bounce", w.h.Deployments)
	}
}

// Past the merge there is no way back. The PR is closed, so
// ClaimReconcile refuses it, and the writer matrix reverts any hand
// move to Merged or Done — so a failure after merging strands the
// ticket in Blocked with its work already on main. ORC-1 hit exactly
// this on the first real merge the pipeline made: the deployment API
// returned 403, the run aborted, and the ticket had nowhere legitimate
// left to go.
func TestReconcileStillReachesMergedWhenTheDeployRecordFails(t *testing.T) {
	w := seedReconciling(t)
	w.cfg.Deploy.Provider = "github"
	w.h.FailRecordDeployment = true

	if err := FinishReconcile(w.ctx, w.p, w.h, w.res, &Verdict{Outcome: "pass"}); err != nil {
		t.Fatalf("a failed deploy record must not fail the run: %v", err)
	}
	w.check(t, protocol.Merged)

	issues, _ := w.tr.ListIssues(w.ctx, w.cfg.Tracker.TeamID, w.cfg.Tracker.ProjectID)
	said := false
	for _, c := range issues[0].Comments {
		if strings.Contains(c.Body, "recording the stand-in deployment failed") {
			said = true
		}
	}
	if !said {
		t.Error("the failure must be on the ticket in words — a silent one looks like a deploy that never happened")
	}
}

// A branch that conflicts with main is a stale branch, not bad work.
// The verdict passed; something else landed first. Before this, the
// merge error aborted the run and the abort sent the ticket to Blocked —
// where only the author can move it — for the one failure in this
// pipeline a dev agent is unambiguously equipped to fix.
func TestUnmergeablePassGoesBackToTheQueue(t *testing.T) {
	w := seedReconciling(t)
	w.h.Unmergeable[5] = true

	err := FinishReconcile(w.ctx, w.p, w.h, w.res, &Verdict{
		Outcome: "pass", Report: "PASS — the diff says what the ticket asked for.",
	})
	if err != nil {
		t.Fatalf("a conflict must not fail the run: %v", err)
	}
	w.check(t, protocol.ReadyForRework)

	issues, _ := w.tr.ListIssues(w.ctx, w.cfg.Tracker.TeamID, w.cfg.Tracker.ProjectID)
	var body string
	var bounces int
	for _, c := range issues[0].Comments {
		m, ok, err := marker.Parse(c.Body)
		if err != nil || !ok {
			continue
		}
		switch m.Kind {
		case marker.MergeConflict:
			body = c.Body
		case marker.ReconcileBounce:
			bounces++
		}
	}
	if body == "" {
		t.Fatal("no merge-conflict marker; the rework has no scope and the count has nothing to read")
	}
	// Counted apart from a reconcile bounce on purpose: that count
	// escalates at two because "two failures to land the same scope is a
	// design problem", and this is not a finding about the work at all.
	if bounces != 0 {
		t.Errorf("a conflict wrote %d reconcile-bounce marker(s) — it would escalate as if the design were wrong", bounces)
	}
	// The dev agent reads a bounce as a finding about the diff unless
	// told otherwise, and would re-litigate a design that just passed.
	if !strings.Contains(body, "Nothing about the work is in question") {
		t.Errorf("the scope does not say the work is fine:\n%s", body)
	}
	if !strings.Contains(body, "origin/main") {
		t.Errorf("the scope does not say what to actually do:\n%s", body)
	}
	// And the PR is still open to push the resolution to.
	if w.h.Merged[5] != "" {
		t.Error("the PR was merged despite the conflict")
	}
}

// Reconciliation answers the re-evaluate flag: it owns the state the
// flag sits in (DESIGN §7) and it is the last thread before the merge.
// The Checks hold that used to stand in for this was a dead end —
// Checks has no agent, so nothing evaluated the flag and a green ticket
// waited on a human.
func TestReconcileSettlesTheCollision(t *testing.T) {
	t.Run("holds: clears the flag and merges on its own verdict", func(t *testing.T) {
		w := seedFlaggedReconciling(t)
		v := &Verdict{Outcome: "pass", Report: "matches", Collision: CollisionHolds}
		if err := FinishReconcile(w.ctx, w.p, w.h, w.res, v); err != nil {
			t.Fatal(err)
		}
		if issue := findIssue(t, w.tr, w.cfg, w.id); hasLabel(issue.Labels, core.LabelReEvaluate) {
			t.Error("the flag survived a settled collision, so a design re-read would judge it twice")
		}
		w.check(t, protocol.Merged)
	})

	t.Run("bites: bounces to rework whatever the outcome says", func(t *testing.T) {
		w := seedFlaggedReconciling(t)
		// A clean pass whose ground has moved still must not merge —
		// which is why collision is its own axis, not a fourth outcome.
		v := &Verdict{Outcome: "pass", Report: "the licensing rule it depends on was rewritten", Collision: CollisionBites}
		if err := FinishReconcile(w.ctx, w.p, w.h, w.res, v); err != nil {
			t.Fatal(err)
		}
		w.check(t, protocol.ReadyForRework)
		if w.h.Merged[5] != "" {
			t.Error("merged under a collision the verdict said still bites")
		}
		issue := findIssue(t, w.tr, w.cfg, w.id)
		if hasLabel(issue.Labels, core.LabelReEvaluate) {
			t.Error("the flag survived; the question has been answered")
		}
		last := issue.Comments[len(issue.Comments)-1].Body
		if !strings.Contains(last, "licensing rule") {
			t.Errorf("the report is not the rework scope:\n%s", last)
		}
		// The scope must say the diff was not wrong when written, or the
		// rework agent re-litigates a design nothing questioned.
		if !strings.Contains(last, "not about your diff being wrong") {
			t.Errorf("the scope reads as a finding against the diff:\n%s", last)
		}
	})

	t.Run("unanswered: bounces rather than merging on an unjudged collision", func(t *testing.T) {
		w := seedFlaggedReconciling(t)
		v := &Verdict{Outcome: "pass", Report: "matches"} // no collision field
		if err := FinishReconcile(w.ctx, w.p, w.h, w.res, v); err != nil {
			t.Fatal(err)
		}
		w.check(t, protocol.ReadyForRework)
		if w.h.Merged[5] != "" {
			t.Error("merged on a collision nobody judged")
		}
	})

	t.Run("unflagged tickets are unaffected", func(t *testing.T) {
		w := seedReconciling(t)
		if err := FinishReconcile(w.ctx, w.p, w.h, w.res, &Verdict{Outcome: "pass"}); err != nil {
			t.Fatal(err)
		}
		w.check(t, protocol.Merged)
	})
}

// seedFlaggedReconciling is seedReconciling with the flag on before the
// claim, which is the only order that matters: the claim is what carries
// the labels into the run.
func seedFlaggedReconciling(t *testing.T) *reconcileWorld {
	t.Helper()
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Flagged work", "The argument.", protocol.Reconciling)
	if err := p.AddTicketLabel(ctx, i.ID, core.LabelReEvaluate); err != nil {
		t.Fatal(err)
	}
	h.PRs = []host.PR{{Number: 5, Branch: strings.ToLower(i.Key) + "-flagged", HeadSHA: "sha5"}}
	res, err := ClaimReconcile(ctx, p, i.Key, "run_91", "https://gh/91", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !hasLabel(res.Labels, core.LabelReEvaluate) {
		t.Fatal("the claim did not carry the flag, so the run cannot answer it")
	}
	return &reconcileWorld{ctx: ctx, tr: tr, h: h, cfg: cfg, p: p, id: i.ID, res: res}
}
