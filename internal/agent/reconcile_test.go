package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/host"
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
