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

	if err := Finish(ctx, p, h, res, "", 3, nil); err == nil {
		t.Error("empty hand-back must be refused")
	}
	if err := Finish(ctx, p, h, res, "Landed the cap screen states. Commit abc123. Left the tooltip out: not in scope.", 3, nil); err != nil {
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
	if err := Finish(ctx, p, h, res2, "hand-back", -1, nil); err != nil {
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

	if err := Abort(ctx, p, res, "pushback", ""); err == nil {
		t.Error("push-back without its argument must be refused")
	}
	if err := Abort(ctx, p, res, "pushback", "The design assumes a socket the static export cannot have."); err != nil {
		t.Fatal(err)
	}
	// Blocked rather than straight back to Designing, and the origin
	// rides along: a design run is dispatched on every entry to Designing
	// and nothing counts the entries, so a decisionless pass and a
	// push-back could hand one ticket between them indefinitely. Parking
	// puts the only participant who can break that in front of it.
	if got := issueState(t, tr, cfg, i.ID); got != protocol.Blocked {
		t.Errorf("state = %q, want blocked", got)
	}
	if got := blockedFrom(t, tr, cfg, i.ID); got != string(protocol.InProgress) {
		t.Errorf("blocked marker from = %q, want %q", got, protocol.InProgress)
	}

	j := seed(t, tr, cfg, "Crash case", "d", protocol.ReadyForDev)
	res2, err := Claim(ctx, p, j.Key, "r", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := Abort(ctx, p, res2, "failed", ""); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, j.ID); got != protocol.Blocked {
		t.Errorf("state = %q, want blocked", got)
	}
	// The origin rides on the comment: only the author moves a ticket out
	// of Blocked and they pick the state, so "In progress" is the whole
	// answer to the question they are being asked.
	if got := blockedFrom(t, tr, cfg, j.ID); got != string(protocol.InProgress) {
		t.Errorf("blocked marker from = %q, want %q", got, protocol.InProgress)
	}
}

// needs-setup is Blocked's third flavor: nothing failed, nothing is owed
// a judgment, a human has to do something the run cannot. It has to be
// distinguishable from a crash on the ticket itself, or the Blocked
// column stops answering "what is broken".
func TestAbortNeedsSetupIsNotAFailure(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	i := seed(t, tr, cfg, "Needs a secret", "d", protocol.ReadyForDev)
	res, err := Claim(ctx, p, i.Key, "r", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := Abort(ctx, p, res, "needs-setup", ""); err == nil {
		t.Error("needs-setup without saying what to set up must be refused — nobody could unblock it")
	}
	if err := Abort(ctx, p, res, "needs-setup", "STRIPE_WEBHOOK_SECRET has to exist in the deploy environment."); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.Blocked {
		t.Errorf("state = %q, want blocked", got)
	}
	issue := findIssue(t, tr, cfg, i.ID)
	found := false
	for _, l := range issue.Labels {
		if l == core.LabelNeedsSetup {
			found = true
		}
	}
	if !found {
		t.Errorf("labels = %v, want needs-setup — otherwise this reads as a crash", issue.Labels)
	}
	if got := blockedFrom(t, tr, cfg, i.ID); got != string(protocol.InProgress) {
		t.Errorf("blocked marker from = %q, want %q", got, protocol.InProgress)
	}
}

// blockedFrom reads the `from` field off the newest blocked marker.
func blockedFrom(t *testing.T, tr *tracker.Memory, cfg *config.Config, id string) string {
	t.Helper()
	var from string
	for _, c := range findIssue(t, tr, cfg, id).Comments {
		m, ok, err := marker.Parse(c.Body)
		if err == nil && ok && m.Kind == marker.Blocked {
			from = m.Fields["from"]
		}
	}
	return from
}

func findIssue(t *testing.T, tr *tracker.Memory, cfg *config.Config, id string) tracker.Issue {
	t.Helper()
	issues, err := tr.ListIssues(context.Background(), cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range issues {
		if i.ID == id {
			return i
		}
	}
	t.Fatalf("no issue %s", id)
	return tracker.Issue{}
}

// A rework claim reads the failing build so the agent doesn't have to.
// It cannot: it holds no GitHub credential by design (DESIGN §9), and
// the failure comment that is its whole scope links a run rather than
// quoting one.
func TestReworkClaimReadsTheFailingBuild(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "Original argument", protocol.ReadyForRework)
	if err := tr.CommentOnIssue(ctx, i.ID, "CI failed.\n\nFailing: gates."); err != nil {
		t.Fatal(err)
	}
	branch := strings.ToLower(i.Key) + "-cap-screen"
	h.PRs = append(h.PRs, host.PR{Number: 4, Branch: branch, HeadSHA: "deadbee"})
	h.CheckState["deadbee"] = host.Checks{Status: host.ChecksRed, RunURL: "https://gh/run/9", FailedJobs: []string{"gates"}}
	h.JobLogs["deadbee"] = []host.JobLog{{Name: "gates", Log: "** (CompileError) undefined function farwell/1"}}

	res, err := Claim(ctx, p, i.Key, "run_9", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.CIFailure == nil {
		t.Fatal("no CI failure on the claim — the agent gets a link it cannot open and nothing else")
	}
	if len(res.CIFailure.Jobs) != 1 || !strings.Contains(res.CIFailure.Jobs[0].Log, "farwell/1") {
		t.Errorf("the failing job's output did not travel: %+v", res.CIFailure.Jobs)
	}
}

// A dev claim on a fresh ticket has no failing build behind it, and must
// not go looking for one — green checks on the design commit are not a
// failure, and a section about a build that didn't fail is noise in the
// prompt at exactly the moment the agent is deciding what to build.
func TestFreshClaimHasNoFailingBuild(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "Original argument", protocol.ReadyForDev)
	branch := strings.ToLower(i.Key) + "-cap-screen"
	h.PRs = append(h.PRs, host.PR{Number: 4, Branch: branch, HeadSHA: "deadbee"})
	h.CheckState["deadbee"] = host.Checks{Status: host.ChecksRed, RunURL: "https://gh/run/9"}

	res, err := Claim(ctx, p, i.Key, "run_9", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.CIFailure != nil {
		t.Errorf("a fresh dev claim carries a CI failure: %+v", res.CIFailure)
	}
}

// Harness findings are the channel agents have for "the pipeline itself
// is broken" — the thing that previously went into hand-back prose and
// reached nobody, because the boundary scan's inputs (DESIGN §10) are
// diffs, TODOs, skipped tests and dependency drift.
func TestHarnessFindingsRoundTripToTheBoundary(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "d", protocol.ReadyForDev)

	found := []HarnessFinding{{
		Title:  "No Base sha is ever recorded",
		Detail: "DESIGN §2.4 makes it optimistic concurrency control checked at pickup; nothing writes it.",
		Dedupe: "base-sha-never-written",
	}}
	if err := PostHarnessFindings(ctx, p, i.ID, found); err != nil {
		t.Fatal(err)
	}
	// Twice, as two runs hitting the same gap would.
	if err := PostHarnessFindings(ctx, p, i.ID, found); err != nil {
		t.Fatal(err)
	}

	snap, err := p.Build(ctx, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	got := CollectHarnessFindings(snap.Tickets)
	if len(got) != 1 {
		t.Fatalf("collected %d findings, want 1 — the dedupe key is what stops ten runs filing ten tickets: %+v", len(got), got)
	}
	if got[0].Title != found[0].Title {
		t.Errorf("title = %q, want %q", got[0].Title, found[0].Title)
	}
	if !strings.Contains(got[0].Detail, "optimistic concurrency control") {
		t.Errorf("the detail did not survive the round trip: %q", got[0].Detail)
	}
}

// A finding nobody can act on costs a read and buys nothing, and one
// without a stable key files itself every run.
func TestHarnessFindingsRefuseTheUnusable(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		path := filepath.Join(dir, "f.json")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if _, err := LoadHarnessFindings(write(`[{"title":"x","dedupe":"k"}]`)); err == nil {
		t.Error("a finding with no detail was accepted")
	}
	if _, err := LoadHarnessFindings(write(`[{"title":"x","detail":"y"}]`)); err == nil {
		t.Error("a finding with no dedupe key was accepted")
	}
	// The normal case: no file at all, which is most runs.
	got, err := LoadHarnessFindings(filepath.Join(dir, "absent.json"))
	if err != nil || got != nil {
		t.Errorf("an absent findings file must be silence, got %v / %v", got, err)
	}
}

// A rework bounced by reconciliation, not by CI, gets no failing-build
// section. This is what ORC-40 actually hit: the claim asked the checks
// API to find out whether CI was red, that call 403'd under the agent's
// PAT, and a rework whose scope was a fully-argued reconcile bounce was
// handed a section saying the harness could not read logs — for a build
// that had never failed.
func TestReconcileBounceReworkHasNoFailingBuildSection(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "Original argument", protocol.ReadyForRework)
	if err := tr.CommentOnIssue(ctx, i.ID, "[pipeline:v1:reconcile-bounce] pr=4\n\nThe claim about deploy detection is false."); err != nil {
		t.Fatal(err)
	}
	branch := strings.ToLower(i.Key) + "-cap-screen"
	h.PRs = append(h.PRs, host.PR{Number: 4, Branch: branch, HeadSHA: "deadbee"})
	// No failing jobs: CI is not why this bounced.

	res, err := Claim(ctx, p, i.Key, "run_9", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.CIFailure != nil {
		t.Errorf("a reconcile bounce carries a CI failure section: %+v", res.CIFailure)
	}
}

// A dev run that correctly changes nothing.
//
// This used to be the worst outcome the pipeline could produce: the host
// rejected a PR with no commits behind it, the finish step exited
// non-zero, and the generic `abort on failure` stamped Blocked/failed on
// top of a hand-back arguing — correctly — that the right answer was to
// change nothing. Measured on ORC-64, where the scope was already on
// main from ORC-23 and every clause of it checked out.
//
// It is a park, not a failure, and not a route the pipeline picks a
// reason for. Which of "duplicate, cancel it" and "stale scope, rewrite
// it" applies is a judgment, and both the hand-back and the label are
// there so a human can make it.
func TestFinishWithNoCommitsAndNoReasonParksTheTicket(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Add a farewell to the home screen", "d", protocol.ReadyForDev)
	res, err := Claim(ctx, p, i.Key, "r", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	handback := "Nothing landed, deliberately: every clause of the scope is already on main."
	if err := Finish(ctx, p, h, res, handback, 0, nil); err != nil {
		t.Fatal(err)
	}

	if got := issueState(t, tr, cfg, i.ID); got != protocol.Blocked {
		t.Errorf("state = %q, want blocked — nothing failed, but nobody but the author can say what this ticket is", got)
	}
	if len(h.PRs) != 0 {
		t.Errorf("opened %d PR(s) with nothing to put in them: %+v", len(h.PRs), h.PRs)
	}
	issue := findIssue(t, tr, cfg, i.ID)
	if !hasLabel(issue.Labels, core.LabelScopeSatisfied) {
		t.Errorf("labels = %v, want %s — same status and same question as a run that named it, so the same label", issue.Labels, core.LabelScopeSatisfied)
	}
	// The run's own account survives, and so does the protocol's. The
	// hand-back is the half that looked at the repository.
	var handbackSeen, explained bool
	for _, c := range issue.Comments {
		if strings.Contains(c.Body, handback) {
			handbackSeen = true
		}
		if strings.Contains(c.Body, "No changes, and no reason given") {
			explained = true
		}
	}
	if !handbackSeen || !explained {
		t.Errorf("hand-back on ticket: %v; explanation on ticket: %v — a park nobody can read is a park nobody acts on", handbackSeen, explained)
	}
}

// Absent and zero are different claims. Reading "nobody counted" as
// "nothing landed" would park a ticket whose work was fine, which is a
// worse failure than the one this check exists for — so it refuses
// rather than assumes, and only where the count decides something.
func TestFinishRefusesToGuessWhetherAnythingLanded(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "d", protocol.ReadyForDev)
	res, err := Claim(ctx, p, i.Key, "r", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	err = Finish(ctx, p, h, res, "hand-back", -1, nil)
	if err == nil || !strings.Contains(err.Error(), "--commits") {
		t.Fatalf("want a refusal naming the flag that fixes it, got %v", err)
	}
	if got := issueState(t, tr, cfg, i.ID); got == protocol.Blocked {
		t.Error("refusing left the ticket parked; it should sit where the run found it")
	}
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// A project set up before the scope-satisfied label existed does not carry
// it, and re-running setup is not something a dev run can wait for. The
// plane provisions the label on demand instead, so the park works on a
// project that has never heard of it — worth pinning, because the
// alternative failure is the one this route was built to stop: a park
// that cannot attach its label becomes a run recorded as broken.
func TestParkWorksOnAProjectMissingTheLabel(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	tr.DropLabel(cfg.Tracker.TeamID, core.LabelScopeSatisfied)
	i := seed(t, tr, cfg, "Already on main", "d", protocol.ReadyForDev)
	res, err := Claim(ctx, p, i.Key, "r", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := Finish(ctx, p, h, res, "Nothing landed; the scope is already on main.", 0, nil); err != nil {
		t.Fatalf("a project without the label could not park a ticket: %v", err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.Blocked {
		t.Errorf("state = %q, want blocked", got)
	}
	if issue := findIssue(t, tr, cfg, i.ID); !hasLabel(issue.Labels, core.LabelScopeSatisfied) {
		t.Errorf("labels = %v, want the label provisioned on demand", issue.Labels)
	}
}

// The three reasons a run legitimately changes nothing, each landing
// somewhere different. Collapsing them was tried first and rejected: a
// duplicate ticket, a missing secret and an unbuildable design want
// three different things from a human, and a Blocked column that renders
// them identically is one where every ticket has to be opened to be
// read.
func TestNamedDevOutcomesRouteSeparately(t *testing.T) {
	cases := []struct {
		outcome string
		state   protocol.State
		label   string
	}{
		{"scope-satisfied", protocol.Blocked, core.LabelScopeSatisfied},
		{"needs-setup", protocol.Blocked, core.LabelNeedsSetup},
		// Blocked, not Designing. Routing straight back to Designing
		// re-dispatches design, and nothing counts the trips: a
		// decisionless pass and a push-back can hand one ticket back and
		// forth forever, each correct on its own terms.
		{"pushback", protocol.Blocked, core.LabelPushback},
	}
	for _, c := range cases {
		t.Run(c.outcome, func(t *testing.T) {
			ctx := context.Background()
			tr, h, cfg, p := world(t)
			i := seed(t, tr, cfg, "Add a farewell", "d", protocol.ReadyForDev)
			res, err := Claim(ctx, p, i.Key, "r", "u", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			o := &DevOutcome{Outcome: c.outcome, Summary: "The argument for " + c.outcome + "."}
			if err := Finish(ctx, p, h, res, "hand-back", 0, o); err != nil {
				t.Fatal(err)
			}
			if got := issueState(t, tr, cfg, i.ID); got != c.state {
				t.Errorf("state = %q, want %q", got, c.state)
			}
			if len(h.PRs) != 0 {
				t.Errorf("opened a PR for a run that changed nothing: %+v", h.PRs)
			}
			issue := findIssue(t, tr, cfg, i.ID)
			if !hasLabel(issue.Labels, c.label) {
				t.Errorf("labels = %v, want %s", issue.Labels, c.label)
			}
			var argued bool
			for _, cm := range issue.Comments {
				if strings.Contains(cm.Body, "The argument for "+c.outcome) {
					argued = true
				}
			}
			if !argued {
				t.Error("the model's argument did not reach the ticket, so nobody can act on it")
			}
		})
	}
}

// An outcome without its argument is a ticket nobody can act on — each
// of the three ends with a human reading it and deciding something.
func TestDevOutcomeRequiresItsArgument(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "outcome.json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := LoadDevOutcome(write(`{"outcome":"pushback"}`)); err == nil {
		t.Error("a push-back with no argument was accepted")
	}
	if _, err := LoadDevOutcome(write(`{"outcome":"nonsense","summary":"x"}`)); err == nil {
		t.Error("an unknown outcome was accepted")
	}
	// Absent means done: an older prompt that never wrote one still
	// finishes, and a run that changed nothing without saying why is
	// caught by the commit count instead.
	o, err := LoadDevOutcome(filepath.Join(dir, "never-written.json"))
	if err != nil || o.Outcome != "done" {
		t.Errorf("missing file = %+v, %v; want done", o, err)
	}
}

// A named outcome says the run changed nothing, so commits contradict
// it. Reported rather than refused: the outcome is the model's own
// account, overriding it would be the harness guessing, and failing here
// would land the ticket in Blocked/failed — the outcome these routes
// exist to avoid.
func TestNamedOutcomeWithCommitsSaysSoRatherThanFailing(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Add a farewell", "d", protocol.ReadyForDev)
	res, err := Claim(ctx, p, i.Key, "r", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o := &DevOutcome{Outcome: "scope-satisfied", Summary: "Already on main."}
	if err := Finish(ctx, p, h, res, "hand-back", 2, o); err != nil {
		t.Fatal(err)
	}
	if got := issueState(t, tr, cfg, i.ID); got != protocol.Blocked {
		t.Errorf("state = %q, want the model's outcome honoured", got)
	}
	issue := findIssue(t, tr, cfg, i.ID)
	var noted bool
	for _, c := range issue.Comments {
		if strings.Contains(c.Body, "left 2 commit(s)") {
			noted = true
		}
	}
	if !noted {
		t.Error("the contradiction is not on the ticket, so the commits are invisible to whoever reads it")
	}
}
