package plane

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/host"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/setup"
	"github.com/SwaggerAllen/orchestration/internal/state"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

// world provisions a fake tracker exactly the way production does: through
// setup. Tests then act on it through the same port the live plane uses.
func world(t *testing.T) (*tracker.Memory, *config.Config, *Plane) {
	t.Helper()
	tr := tracker.NewMemory()
	cfg := config.Sample()
	if _, err := setup.Run(context.Background(), tr, cfg, false); err != nil {
		t.Fatal(err)
	}
	return tr, cfg, New(tr, cfg).WithState(state.NewMemory())
}

// recordSeed writes what the harness would have written when it put a
// ticket in this state. Without it the ticket carries no record and is
// judged on nothing, which is correct behaviour and a useless fixture.
func recordSeed(t *testing.T, p *Plane, id string, s protocol.State) {
	t.Helper()
	if err := p.State.Record(context.Background(), id, core.RecordedMove{To: s, Role: core.RoleControlPlane}); err != nil {
		t.Fatal(err)
	}
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

func seedIssue(t *testing.T, tr *tracker.Memory, cfg *config.Config, title string, s protocol.State) tracker.Issue {
	t.Helper()
	i, err := tr.CreateIssue(context.Background(), tracker.NewIssue{
		TeamID: cfg.Tracker.TeamID, ProjectID: cfg.Tracker.ProjectID,
		Title: title, StateID: stateID(t, tr, cfg, s),
	})
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestBuildTranslatesStatesRolesAndHistory(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)

	i := seedIssue(t, tr, cfg, "Rework the cap screen", protocol.DesignReview)

	// The author signs off: a transition performed by the author's id.
	tr.ActorID = "usr_author"
	if err := tr.UpdateIssueState(ctx, i.ID, stateID(t, tr, cfg, protocol.ReadyForDev)); err != nil {
		t.Fatal(err)
	}
	tr.ActorID = "memory-bot"

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Tickets) != 1 {
		t.Fatalf("tickets = %d", len(snap.Tickets))
	}
	tk := snap.Tickets[0]
	if tk.State != protocol.ReadyForDev {
		t.Errorf("state = %q", tk.State)
	}
	if tk.Last == nil || tk.Last.Actor != core.RoleAuthor || tk.Last.From != protocol.DesignReview {
		t.Errorf("last transition = %+v, want author from design_review", tk.Last)
	}
}

func TestBuildFailsOnUnmappedState(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)

	rogue, err := tr.CreateState(ctx, cfg.Tracker.TeamID, tracker.NewState{Name: "Triage Later", Category: protocol.CategoryStarted})
	if err != nil {
		t.Fatal(err)
	}
	i := seedIssue(t, tr, cfg, "Lost ticket", protocol.Todo)
	if err := tr.UpdateIssueState(ctx, i.ID, rogue.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := p.Build(ctx, time.Now(), false); err == nil || !strings.Contains(err.Error(), "doesn't map") {
		t.Errorf("want unmapped-state error, got %v", err)
	}
}

func TestBuildCurrentMilestone(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)

	m1 := tr.AddMilestone(cfg.Tracker.ProjectID, "M: alpha", 1)
	tr.AddMilestone(cfg.Tracker.ProjectID, "M: beta", 2)

	i := seedIssue(t, tr, cfg, "Only ticket", protocol.Done)
	if err := tr.Mutate(i.ID, func(is *tracker.Issue) { is.Milestone = "M: alpha" }); err != nil {
		t.Fatal(err)
	}
	_ = m1

	// Drained milestone without a Done boundary is still current.
	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	if snap.CurrentMilestone != "M: alpha" {
		t.Errorf("current = %q, want M: alpha", snap.CurrentMilestone)
	}
}

// The M2 composition test: seed an illegal promotion in the fake tracker,
// then Build -> Sweep -> Execute -> Build and watch the revert land as
// tracker state — the same loop the live control plane runs.
func TestLiveShapedLoopRevertsIllegalPromotion(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)

	inFlight := seedIssue(t, tr, cfg, "Holder", protocol.InProgress)
	if _, err := tr.CreateLabel(ctx, cfg.Tracker.TeamID, "screen:home"); err != nil {
		t.Fatal(err)
	}
	if err := tr.AddIssueLabel(ctx, cfg.Tracker.TeamID, inFlight.ID, "screen:home"); err != nil {
		t.Fatal(err)
	}

	victim := seedIssue(t, tr, cfg, "Collider", protocol.DesignReview)
	// The design pass left it here, and said so. The author's move below
	// is then a divergence from that record — which is the whole way the
	// sweep now tells the two apart.
	recordSeed(t, p, victim.ID, protocol.DesignReview)
	if err := tr.AddIssueLabel(ctx, cfg.Tracker.TeamID, victim.ID, "screen:home"); err != nil {
		t.Fatal(err)
	}
	tr.ActorID = "usr_author"
	if err := tr.UpdateIssueState(ctx, victim.ID, stateID(t, tr, cfg, protocol.ReadyForDev)); err != nil {
		t.Fatal(err)
	}
	tr.ActorID = "memory-bot"

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	acts := core.Sweep(snap)
	var log bytes.Buffer
	if err := p.Execute(ctx, acts, &log); err != nil {
		t.Fatal(err)
	}

	after, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range after.Tickets {
		if tk.ID != victim.ID {
			continue
		}
		if tk.State != protocol.DesignReview {
			t.Errorf("victim state = %q, want reverted to design_review", tk.State)
		}
		found := false
		for _, c := range tk.Comments {
			if strings.Contains(c.Body, "[pipeline:v1:revert]") {
				found = true
			}
		}
		if !found {
			t.Error("revert marker comment missing")
		}
	}
}

func TestExecuteCreatesBoundaryTicket(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)

	tr.AddMilestone(cfg.Tracker.ProjectID, "M: alpha", 1)
	i := seedIssue(t, tr, cfg, "Last one", protocol.Done)
	if err := tr.Mutate(i.ID, func(is *tracker.Issue) { is.Milestone = "M: alpha" }); err != nil {
		t.Fatal(err)
	}

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	acts := core.Sweep(snap)
	var log bytes.Buffer
	if err := p.Execute(ctx, acts, &log); err != nil {
		t.Fatal(err)
	}

	issues, err := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	var boundary *tracker.Issue
	for idx := range issues {
		for _, l := range issues[idx].Labels {
			if l == "milestone-boundary" {
				boundary = &issues[idx]
			}
		}
	}
	if boundary == nil {
		t.Fatal("boundary ticket not created")
	}
	if boundary.Milestone != "M: alpha" || !strings.Contains(boundary.Description, "pipeline machinery") {
		t.Errorf("boundary = %+v", boundary)
	}

	// Second loop: boundary exists, so nothing new is planned.
	snap2, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range core.Sweep(snap2) {
		if a.Kind == core.ActCreateBoundary {
			t.Errorf("boundary planned twice: %v", a)
		}
	}
}

// A proposal in Linear's Triage is not a pipeline ticket. The boundary
// agent files its findings there (DESIGN §10) and the author accepts or
// declines them; until then there is no state for the sweep to reason
// about, so the snapshot skips them.
//
// This was fatal, and the boundary agent is the one thing that creates
// such issues — so it broke its own next snapshot by doing its job:
// three proposals filed, all three steps green, then dead on the first
// one it read back, and the ticket Blocked with every step's work done.
func TestBuildSkipsTriageProposals(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)

	triage, err := tr.CreateState(ctx, cfg.Tracker.TeamID, tracker.NewState{Name: "Triage", Category: protocol.CategoryTriage})
	if err != nil {
		t.Fatal(err)
	}
	kept := seedIssue(t, tr, cfg, "Real ticket", protocol.Todo)
	proposal := seedIssue(t, tr, cfg, "Boundary finding", protocol.Todo)
	if err := tr.UpdateIssueState(ctx, proposal.ID, triage.ID); err != nil {
		t.Fatal(err)
	}

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatalf("a filed proposal broke the snapshot: %v", err)
	}
	for _, tk := range snap.Tickets {
		if tk.ID == proposal.ID {
			t.Error("a triage proposal is in the snapshot — the sweep could dispatch against a finding nobody accepted")
		}
	}
	found := false
	for _, tk := range snap.Tickets {
		if tk.ID == kept.ID {
			found = true
		}
	}
	if !found {
		t.Error("the real ticket went missing — triage skipping must not swallow the queue")
	}
}

// A proposal about a path no agent can land a change to is labelled as
// it is filed, so it never reaches a queue that would dispatch it into
// a rejected push.
//
// The push token carries no `workflow` scope on any GitHub repository,
// and the rejected push takes the whole run down with it, hand-back
// included — so "the dev agent tries and finds out" is the one outcome
// worth spending a label to avoid.
func TestFileTriageProposalMarksAuthorOnlySubjects(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)

	cases := []struct {
		subject string
		want    bool
	}{
		{".github/workflows/ci.yml", true},
		{".github/workflows/pipeline-agent-dev.yml", true},
		{"pipeline.config.json", true},
		// Back-quoted the way a prompt renders a path.
		{"`.github/workflows/ci.yml`", true},
		// Ordinary subjects, which are most of them.
		{"lib/catapult/foundation.ex", false},
		{"qualityGates", false},
		{"mix xref graph --label compile-connected", false},
		{"", false},
	}
	for _, c := range cases {
		title := "proposal about " + c.subject
		if err := p.FileTriageProposal(ctx, title, "why", "debt", c.subject, false, "m/"+c.subject); err != nil {
			t.Fatalf("%q: %v", c.subject, err)
		}
		issues, err := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		var got bool
		for _, i := range issues {
			if i.Title != title {
				continue
			}
			for _, l := range i.Labels {
				if l == core.LabelAuthorOnly {
					got = true
				}
			}
		}
		if got != c.want {
			t.Errorf("subject %q: author-only = %v, want %v", c.subject, got, c.want)
		}
	}
}

// The label is provisioned on the way through rather than passed into
// the create. Creating an issue with a label the team does not carry
// fails the whole create — which is how a boundary loses a proposal it
// spent a model run computing — while attaching one afterwards goes
// through the path that creates it on demand.
func TestFileTriageProposalSurvivesAMissingAuthorOnlyLabel(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)
	tr.DropLabel(cfg.Tracker.TeamID, core.LabelAuthorOnly)

	if err := p.FileTriageProposal(ctx, "Arm the gates", "why", "debt", ".github/workflows/ci.yml", true, "m/gates"); err != nil {
		t.Fatalf("a project without the label lost the proposal: %v", err)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	var labelled bool
	for _, i := range issues {
		for _, l := range i.Labels {
			if l == core.LabelAuthorOnly {
				labelled = true
			}
		}
	}
	if !labelled {
		t.Error("the label was not provisioned on demand, so the ticket would be dispatched")
	}
}

// The dedupe set is "what this project has already filed", not "what is
// still sitting in Triage". Filtering to triage-category states meant a
// proposal dropped out of the set the moment the author accepted it and
// moved it into the queue — so the next scan that found the same thing
// filed it a second time. Invisible while a milestone had one boundary
// pass; routine once a second pass over the same milestone became an
// ordinary thing to ask for.
func TestFiledProposalsStayDedupedAfterLeavingTriage(t *testing.T) {
	ctx := context.Background()
	tr := tracker.NewMemory()
	cfg := config.Sample()
	if _, err := setup.Run(ctx, tr, cfg, false); err != nil {
		t.Fatal(err)
	}
	p := New(tr, cfg)

	if err := p.FileTriageProposal(ctx, "Extract the cap module", "why", "debt", "lib/cap.ex", false, "M1/extract-cap"); err != nil {
		t.Fatal(err)
	}
	issues, err := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	// The author accepts it: out of intake and into the queue.
	for _, i := range issues {
		if strings.Contains(i.Description, "M1/extract-cap") {
			id, err := p.StateIDFor(ctx, protocol.ReadyForDev)
			if err != nil {
				t.Fatal(err)
			}
			if err := tr.UpdateIssueState(ctx, i.ID, id); err != nil {
				t.Fatal(err)
			}
		}
	}

	filed, err := p.ListTriageProposals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range filed {
		if f.Dedupe == "M1/extract-cap" {
			found = true
		}
	}
	if !found {
		t.Errorf("an accepted proposal left the dedupe set, so the next scan would file it again: %+v", filed)
	}
}

// One ticket in an unmapped state used to make the whole project's
// snapshot unreadable. Marking ORC-47 with Linear's built-in Duplicate
// state — two taps in the UI, not a misconfiguration — took every sweep
// on Catapult down for about two hours and roughly thirty runs.
//
// A resolved state the config does not name is still readable: its
// category answers the only question the pipeline has, which is that the
// ticket is finished and not in the queue. Failing loudly stays the rule
// where guessing could put work in the queue nobody put there.
func TestBuildReadsUnmappedResolvedStatesByCategory(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)

	dup, err := tr.CreateState(ctx, cfg.Tracker.TeamID, tracker.NewState{Name: "Duplicate", Category: protocol.CategoryCanceled})
	if err != nil {
		t.Fatal(err)
	}
	shipped, err := tr.CreateState(ctx, cfg.Tracker.TeamID, tracker.NewState{Name: "Released", Category: protocol.CategoryCompleted})
	if err != nil {
		t.Fatal(err)
	}
	duplicate := seedIssue(t, tr, cfg, "Filed twice", protocol.Todo)
	released := seedIssue(t, tr, cfg, "Out the door", protocol.Todo)
	bystander := seedIssue(t, tr, cfg, "Ordinary work", protocol.Todo)
	if err := tr.UpdateIssueState(ctx, duplicate.ID, dup.ID); err != nil {
		t.Fatal(err)
	}
	if err := tr.UpdateIssueState(ctx, released.ID, shipped.ID); err != nil {
		t.Fatal(err)
	}

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatalf("one ticket's state broke the whole snapshot: %v", err)
	}
	want := map[string]protocol.State{
		duplicate.ID: protocol.Canceled,
		released.ID:  protocol.Done,
		bystander.ID: protocol.Todo,
	}
	got := map[string]protocol.State{}
	for _, tk := range snap.Tickets {
		got[tk.ID] = tk.State
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s read as %q, want %q", id, got[id], w)
		}
	}
}

// One ticket's unreadable CI verdict costs that ticket its verdict, and
// nothing else. It used to cost the whole snapshot, and therefore every
// claim in the project: Catapult's ORC-7 design claim died building a
// snapshot over a 403 reading ORC-5's checks — a ticket it had no
// interest in — and sat 23 minutes in a state that said an agent was
// working on it.
//
// The rule the fix states: project-wide reads are fatal, per-ticket
// reads degrade. attachDeployFacts already worked this way; this one
// did not.
func TestBuildDegradesOneTicketsUnreadableVerdict(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)
	h := host.NewMemory()
	p = p.WithHost(h)

	broken := seedIssue(t, tr, cfg, "In checks, unreadable", protocol.Checks)
	fine := seedIssue(t, tr, cfg, "In checks, readable", protocol.Checks)
	queued := seedIssue(t, tr, cfg, "Waiting to be claimed", protocol.ReadyForDev)

	h.PRs = []host.PR{
		{Number: 1, Branch: broken.Key + "-a", HeadSHA: "sha-broken"},
		{Number: 2, Branch: fine.Key + "-b", HeadSHA: "sha-fine"},
	}
	h.FailChecksFor["sha-broken"] = true
	h.CheckState["sha-fine"] = host.Checks{Status: host.ChecksGreen, RunURL: "https://gh/2"}

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatalf("one ticket's 403 took the whole snapshot down: %v", err)
	}

	got := map[string]*core.Ticket{}
	for _, tk := range snap.Tickets {
		got[tk.ID] = tk
	}
	// Unjudged, which the core waits on rather than acting against.
	if s := got[broken.ID].CI.Status; s != "" {
		t.Errorf("the unreadable ticket carries a verdict %q — it must carry none", s)
	}
	if s := got[fine.ID].CI.Status; s != core.CIGreen {
		t.Errorf("the readable ticket lost its verdict: %q", s)
	}
	if got[queued.ID] == nil {
		t.Error("a ticket with no PR at all went missing from the snapshot")
	}
}
