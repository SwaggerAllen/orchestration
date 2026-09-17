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
	"github.com/SwaggerAllen/orchestration/internal/marker"
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
		if _, err := p.FileTriageProposal(ctx, title, "why", "debt", c.subject, false); err != nil {
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

	if _, err := p.FileTriageProposal(ctx, "Arm the gates", "why", "debt", ".github/workflows/ci.yml", true); err != nil {
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

// One ticket in an unmapped state used to make the whole project's
// snapshot unreadable — every sweep on Catapult down for about two hours
// and roughly thirty runs, on a tracker action that looks like
// housekeeping.
//
// A resolved state the config does not name is still readable: its
// category answers the only question the pipeline has, which is that the
// ticket is finished and not in the queue. Failing loudly stays the rule
// where guessing could put work in the queue nobody put there.
//
// The states here are a team's own additions, which is what makes them
// unmapped while carrying a category that settles them. Linear's
// built-in Duplicate is a *third* category and has its own test — this
// one used to claim it, under a state named "Duplicate" that was seeded
// `canceled`, and claiming it is how it went unnoticed.
func TestBuildReadsUnmappedResolvedStatesByCategory(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)

	wontDo, err := tr.CreateState(ctx, cfg.Tracker.TeamID, tracker.NewState{Name: "Won't do", Category: protocol.CategoryCanceled})
	if err != nil {
		t.Fatal(err)
	}
	shipped, err := tr.CreateState(ctx, cfg.Tracker.TeamID, tracker.NewState{Name: "Released", Category: protocol.CategoryCompleted})
	if err != nil {
		t.Fatal(err)
	}
	declined := seedIssue(t, tr, cfg, "Not doing this", protocol.Todo)
	released := seedIssue(t, tr, cfg, "Out the door", protocol.Todo)
	bystander := seedIssue(t, tr, cfg, "Ordinary work", protocol.Todo)
	if err := tr.UpdateIssueState(ctx, declined.ID, wontDo.ID); err != nil {
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
		declined.ID:  protocol.Canceled,
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

// Linear's built-in Duplicate carries its own category, not `canceled`,
// and that is the state the ORC-47 incident was actually about: two taps
// in the UI took every sweep on Catapult down for two hours.
//
// The fix for that incident read an unmapped state by category and was
// believed to cover this. It did not. `TestBuildReadsUnmappedResolvedStatesByCategory`
// names its state "Duplicate" but seeds it as `CategoryCanceled`, and it
// could not do otherwise — the category did not exist and the in-memory
// tracker rejected the real value. So the fake and the plane agreed with
// each other while both disagreed with Linear, and the test passed on a
// state Linear never produces.
func TestBuildReadsLinearsOwnDuplicateCategory(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)

	dup, err := tr.CreateState(ctx, cfg.Tracker.TeamID,
		tracker.NewState{Name: "Duplicate", Category: protocol.CategoryDuplicate})
	if err != nil {
		t.Fatal(err)
	}
	filedTwice := seedIssue(t, tr, cfg, "Filed twice", protocol.Todo)
	bystander := seedIssue(t, tr, cfg, "Ordinary work", protocol.Todo)
	if err := tr.UpdateIssueState(ctx, filedTwice.ID, dup.ID); err != nil {
		t.Fatal(err)
	}

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatalf("one ticket marked Duplicate broke the whole project's snapshot: %v", err)
	}
	got := map[string]protocol.State{}
	for _, tk := range snap.Tickets {
		got[tk.ID] = tk.State
	}
	// Canceled rather than Done: a duplicate was discarded, not finished.
	if got[filedTwice.ID] != protocol.Canceled {
		t.Errorf("the duplicate read as %q, want %q", got[filedTwice.ID], protocol.Canceled)
	}
	if got[bystander.ID] != protocol.Todo {
		t.Errorf("the bystander read as %q, want %q", got[bystander.ID], protocol.Todo)
	}
}

// The snapshot reads the preview only where the sweep acts on it, and
// stops paying once the ticket carries the URL. Bounded the same way the
// CI verdict is, and for the same reason: one extra host call per ticket
// in one state is affordable, one per ticket is not.
func TestBuildReadsThePreviewOnlyForUnannouncedDesignReview(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)
	h := host.NewMemory()
	p = p.WithHost(h)

	waiting := seedIssue(t, tr, cfg, "In design review, no preview yet", protocol.DesignReview)
	told := seedIssue(t, tr, cfg, "In design review, already told", protocol.DesignReview)
	elsewhere := seedIssue(t, tr, cfg, "Not in design review", protocol.ReadyForDev)

	h.PRs = []host.PR{
		{Number: 1, Branch: waiting.Key + "-a", HeadSHA: "sha-a"},
		{Number: 2, Branch: told.Key + "-b", HeadSHA: "sha-b"},
		{Number: 3, Branch: elsewhere.Key + "-c", HeadSHA: "sha-c"},
	}
	h.Previews[waiting.Key+"-a"] = host.Preview{Status: host.PreviewReady, URL: "https://a.example.dev"}
	h.Previews[told.Key+"-b"] = host.Preview{Status: host.PreviewReady, URL: "https://b.example.dev"}
	h.Previews[elsewhere.Key+"-c"] = host.Preview{Status: host.PreviewReady, URL: "https://c.example.dev"}

	m := marker.Marker{Kind: marker.Preview, Fields: map[string]string{"url": "https://b.example.dev"}}
	if err := tr.CommentOnIssue(ctx, told.ID, m.Comment("Preview for this pass")); err != nil {
		t.Fatal(err)
	}

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*core.Ticket{}
	for _, tk := range snap.Tickets {
		got[tk.ID] = tk
	}
	if s := got[waiting.ID].Preview.State; s != core.PreviewReady {
		t.Errorf("the ticket waiting on a preview carries %q, want ready", s)
	}
	if got[waiting.ID].Preview.URL != "https://a.example.dev" {
		t.Errorf("preview URL = %q", got[waiting.ID].Preview.URL)
	}
	// Not read, because there is nothing left to say. The core would say
	// nothing either — it checks the same marker — but paying for the
	// call every hour for as long as a ticket sits in review is the cost
	// this skip exists for.
	if s := got[told.ID].Preview.State; s != core.PreviewUnknown {
		t.Errorf("read the preview for a ticket already told about it: %q", s)
	}
	if s := got[elsewhere.ID].Preview.State; s != core.PreviewUnknown {
		t.Errorf("read the preview for a ticket outside Design review: %q", s)
	}
}

// One ticket's unreadable preview costs that ticket a comment, not every
// other ticket its facts — the rule this file already settled for CI
// verdicts, after Catapult's ORC-7 died building a snapshot over a 403 on
// a ticket it had no interest in.
func TestBuildDegradesOneTicketsUnreadablePreview(t *testing.T) {
	ctx := context.Background()
	tr, cfg, p := world(t)
	h := host.NewMemory()
	p = p.WithHost(h)

	broken := seedIssue(t, tr, cfg, "Preview unreadable", protocol.DesignReview)
	fine := seedIssue(t, tr, cfg, "Preview readable", protocol.DesignReview)

	h.PRs = []host.PR{
		{Number: 1, Branch: broken.Key + "-a", HeadSHA: "sha-a"},
		{Number: 2, Branch: fine.Key + "-b", HeadSHA: "sha-b"},
	}
	h.FailPreviewFor[broken.Key+"-a"] = true
	h.Previews[fine.Key+"-b"] = host.Preview{Status: host.PreviewReady, URL: "https://b.example.dev"}

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatalf("one ticket's unreadable preview took the whole snapshot down: %v", err)
	}
	got := map[string]*core.Ticket{}
	for _, tk := range snap.Tickets {
		got[tk.ID] = tk
	}
	if s := got[broken.ID].Preview.State; s != core.PreviewUnknown {
		t.Errorf("the unreadable ticket carries %q — it must carry nothing", s)
	}
	if s := got[fine.ID].Preview.State; s != core.PreviewReady {
		t.Errorf("the readable ticket lost its preview too: %q", s)
	}
}

// The crossing from what the host reports to what the core acts on.
//
// Asserted as a table rather than through the ready case alone, because
// this is the shape CLAUDE.md records for the host-to-core outcome
// mapping: the adapter's tests proved the host's answer was read, the core
// tests proved the rule acted on a state, and the link between them was
// unasserted — so breaking it printed ok. Dropping the pending arm here
// does exactly that: it silently converts "still building" into "nothing
// to say", which is the difference between a bounded wait and a ticket
// that never gets told.
func TestBuildCarriesEveryPreviewStateToTheCore(t *testing.T) {
	for _, tc := range []struct {
		name string
		from host.Preview
		want core.PreviewInfo
	}{
		{"ready", host.Preview{Status: host.PreviewReady, URL: "https://a.example.dev"},
			core.PreviewInfo{State: core.PreviewReady, URL: "https://a.example.dev"}},
		{"pending", host.Preview{Status: host.PreviewPending, Description: "building"},
			core.PreviewInfo{State: core.PreviewPending, Why: "building"}},
		{"failed", host.Preview{Status: host.PreviewFailed, Description: "exited 1"},
			core.PreviewInfo{State: core.PreviewFailed, Why: "exited 1"}},
		{"none", host.Preview{Status: host.PreviewNone}, core.PreviewInfo{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tr, cfg, p := world(t)
			h := host.NewMemory()
			p = p.WithHost(h)

			iss := seedIssue(t, tr, cfg, "In design review", protocol.DesignReview)
			h.PRs = []host.PR{{Number: 1, Branch: iss.Key + "-a", HeadSHA: "sha-a"}}
			h.Previews[iss.Key+"-a"] = tc.from

			snap, err := p.Build(ctx, time.Now(), false)
			if err != nil {
				t.Fatal(err)
			}
			for _, tk := range snap.Tickets {
				if tk.ID != iss.ID {
					continue
				}
				if tk.Preview != tc.want {
					t.Errorf("host %+v -> core %+v, want %+v", tc.from, tk.Preview, tc.want)
				}
				return
			}
			t.Fatal("ticket missing from the snapshot")
		})
	}
}
