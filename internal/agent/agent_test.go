package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/plane"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/setup"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

func world(t *testing.T) (*tracker.Memory, *host.Memory, *config.Config, *plane.Plane) {
	t.Helper()
	tr := tracker.NewMemory()
	h := host.NewMemory()
	cfg := config.Sample()
	if _, err := setup.Run(context.Background(), tr, cfg, false); err != nil {
		t.Fatal(err)
	}
	return tr, h, cfg, plane.New(tr, cfg).WithHost(h)
}

func stateID(t *testing.T, tr *tracker.Memory, cfg *config.Config, s protocol.State) string {
	t.Helper()
	states, err := tr.ListStates(context.Background(), cfg.Tracker.TeamID)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if st.Name == cfg.StateName(s) {
			return st.ID
		}
	}
	t.Fatalf("no state %q", s)
	return ""
}

func seed(t *testing.T, tr *tracker.Memory, cfg *config.Config, title, desc string, s protocol.State) tracker.Issue {
	t.Helper()
	i, err := tr.CreateIssue(context.Background(), tracker.NewIssue{
		TeamID: cfg.Tracker.TeamID, ProjectID: cfg.Tracker.ProjectID,
		Title: title, Description: desc, StateID: stateID(t, tr, cfg, s),
	})
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func issueState(t *testing.T, tr *tracker.Memory, cfg *config.Config, id string) protocol.State {
	t.Helper()
	issues, err := tr.ListIssues(context.Background(), cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	states, _ := tr.ListStates(context.Background(), cfg.Tracker.TeamID)
	nameByID := map[string]string{}
	for _, s := range states {
		nameByID[s.ID] = s.Name
	}
	protoByName := map[string]protocol.State{}
	for _, ps := range protocol.AllStates {
		protoByName[cfg.StateName(ps)] = ps
	}
	for _, i := range issues {
		if i.ID == id {
			return protoByName[nameByID[i.StateID]]
		}
	}
	t.Fatalf("no issue %s", id)
	return ""
}

func TestClaimFreshTicket(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	i := seed(t, tr, cfg, "Rework the cap screen", "The argument.\n\nBase: abc1234\n", protocol.ReadyForDev)

	res, err := Claim(ctx, p, i.Key, "run_77", "https://gh/run/77", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "dev" || res.Scope != res.Description || res.BaseSHA != "abc1234" {
		t.Errorf("result = %+v", res)
	}
	if !strings.HasPrefix(res.Branch, strings.ToLower(i.Key)+"-") {
		t.Errorf("branch %q must start with the issue key", res.Branch)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.InProgress {
		t.Errorf("state = %q, want in_progress", got)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if len(issues[0].Comments) != 1 || !strings.Contains(issues[0].Comments[0].Body, "[pipeline:v1:dispatch]") {
		t.Errorf("dispatch marker missing: %+v", issues[0].Comments)
	}
}

func TestClaimReworkUsesNewestComment(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "Original argument", protocol.ReadyForRework)
	if err := tr.CommentOnIssue(ctx, i.ID, "old comment"); err != nil {
		t.Fatal(err)
	}
	tr.Now = func() time.Time { return time.Now().Add(time.Minute) }
	if err := tr.CommentOnIssue(ctx, i.ID, "[pipeline:v1:reconcile-bounce] missing=copy\n\nThe cap_reached copy did not land."); err != nil {
		t.Fatal(err)
	}

	res, err := Claim(ctx, p, i.Key, "run_78", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "rework" || !strings.Contains(res.Scope, "cap_reached copy") {
		t.Errorf("scope = %q (mode %s), want the newest comment", res.Scope, res.Mode)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.Reworking {
		t.Errorf("state = %q, want reworking", got)
	}
}

func TestClaimRefusalsWriteNothing(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)

	flagged := seed(t, tr, cfg, "Flagged", "d", protocol.ReadyForDev)
	if err := tr.AddIssueLabel(ctx, cfg.Tracker.TeamID, flagged.ID, "re-evaluate"); err != nil {
		t.Fatal(err)
	}
	if _, err := Claim(ctx, p, flagged.Key, "r", "u", time.Now()); err == nil || !strings.Contains(err.Error(), "re-evaluate") {
		t.Errorf("want re-evaluate refusal, got %v", err)
	}
	if got := issueState(t, tr, cfg, flagged.ID); got != protocol.ReadyForDev {
		t.Errorf("refused claim mutated state to %q", got)
	}

	wrong := seed(t, tr, cfg, "Wrong state", "d", protocol.Designing)
	if _, err := Claim(ctx, p, wrong.Key, "r", "u", time.Now()); err == nil || !strings.Contains(err.Error(), "queues only") {
		t.Errorf("want wrong-state refusal, got %v", err)
	}

	empty := seed(t, tr, cfg, "Rework no scope", "d", protocol.ReadyForRework)
	if _, err := Claim(ctx, p, empty.Key, "r", "u", time.Now()); err == nil || !strings.Contains(err.Error(), "newest comment is the scope") {
		t.Errorf("want scopeless-rework refusal, got %v", err)
	}
}

func TestFinishCreatesPROrUndraftsAndMovesToChecks(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "d", protocol.ReadyForDev)
	res, err := Claim(ctx, p, i.Key, "r", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if err := Finish(ctx, p, h, res, ""); err == nil {
		t.Error("empty hand-back must be refused")
	}
	if err := Finish(ctx, p, h, res, "Landed the cap screen states. Commit abc123. Left the tooltip out: not in scope."); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.Checks {
		t.Errorf("state = %q, want checks", got)
	}
	if len(h.PRs) != 1 || h.PRs[0].Draft {
		t.Errorf("PR = %+v, want one non-draft PR", h.PRs)
	}
	if h.PRs[0].Branch != res.Branch {
		t.Errorf("PR branch %q != claimed branch %q", h.PRs[0].Branch, res.Branch)
	}

	// Existing draft PR path: design opened it; finish undrafts it.
	j := seed(t, tr, cfg, "Second ticket", "d", protocol.ReadyForDev)
	draft, err := h.CreatePR(ctx, strings.ToLower(j.Key)+"-second", "draft", "", true)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := Claim(ctx, p, j.Key, "r2", "u2", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res2.PRNumber != draft.Number {
		t.Fatalf("claim did not find the draft PR: %+v", res2)
	}
	if err := Finish(ctx, p, h, res2, "hand-back"); err != nil {
		t.Fatal(err)
	}
	for _, pr := range h.PRs {
		if pr.Number == draft.Number && pr.Draft {
			t.Error("draft flag not flipped")
		}
	}
}

func TestAbortRoutes(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	i := seed(t, tr, cfg, "Unbuildable", "d", protocol.ReadyForDev)
	res, err := Claim(ctx, p, i.Key, "r", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if err := Abort(ctx, p, res.TicketID, "pushback", ""); err == nil {
		t.Error("push-back without its argument must be refused")
	}
	if err := Abort(ctx, p, res.TicketID, "pushback", "The design assumes a socket the static export cannot have."); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.Designing {
		t.Errorf("state = %q, want designing", got)
	}

	j := seed(t, tr, cfg, "Crash case", "d", protocol.ReadyForDev)
	res2, err := Claim(ctx, p, j.Key, "r", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := Abort(ctx, p, res2.TicketID, "failed", ""); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, j.ID); got != protocol.Blocked {
		t.Errorf("state = %q, want blocked", got)
	}
}
