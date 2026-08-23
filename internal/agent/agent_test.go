package agent

import (
	"context"
	"fmt"
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

	if err := Finish(ctx, p, h, res, "", 3, nil, nil); err == nil {
		t.Error("empty hand-back must be refused")
	}
	if err := Finish(ctx, p, h, res, "Landed the cap screen states. Commit abc123. Left the tooltip out: not in scope.", 3, nil, nil); err != nil {
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
	if err := Finish(ctx, p, h, res2, "hand-back", -1, nil, nil); err != nil {
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

	if err := Abort(ctx, p, res, "pushback", "", ""); err == nil {
		t.Error("push-back without its argument must be refused")
	}
	if err := Abort(ctx, p, res, "pushback", "The design assumes a socket the static export cannot have.", ""); err != nil {
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
	if err := Abort(ctx, p, res2, "failed", "", ""); err != nil {
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
	if err := Abort(ctx, p, res, "needs-setup", "", ""); err == nil {
		t.Error("needs-setup without saying what to set up must be refused — nobody could unblock it")
	}
	if err := Abort(ctx, p, res, "needs-setup", "STRIPE_WEBHOOK_SECRET has to exist in the deploy environment.", ""); err != nil {
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
	if err := Finish(ctx, p, h, res, handback, 0, nil, nil); err != nil {
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
	err = Finish(ctx, p, h, res, "hand-back", -1, nil, nil)
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
	if err := Finish(ctx, p, h, res, "Nothing landed; the scope is already on main.", 0, nil, nil); err != nil {
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
		// A fact about the token, not a judgment: the push carries no
		// workflow scope, so a commit touching .github/workflows is
		// rejected and the rejection takes the run down with it. No
		// label on this ticket — the author-only half is split off into
		// its own, which TestAuthorOnlyRunSplitsOffItsBlocker covers.
		{"author-only", protocol.Blocked, ""},
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
			if err := Finish(ctx, p, h, res, "hand-back", 0, o, nil); err != nil {
				t.Fatal(err)
			}
			if got := issueState(t, tr, cfg, i.ID); got != c.state {
				t.Errorf("state = %q, want %q", got, c.state)
			}
			if len(h.PRs) != 0 {
				t.Errorf("opened a PR for a run that changed nothing: %+v", h.PRs)
			}
			issue := findIssue(t, tr, cfg, i.ID)
			if c.label != "" && !hasLabel(issue.Labels, c.label) {
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
	if err := Finish(ctx, p, h, res, "hand-back", 2, o, nil); err != nil {
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

// The dev-run escape hatch (DESIGN §8, §12). A run that finds its scope
// needs a path no agent can land a change to splits the ticket in two:
// the author-only half is filed on its own and blocks the original,
// which stays the pipeline's.
//
// Labelling the original was the first shape. The label makes the
// pipeline route around a ticket in every state, so it would have
// retired work the pipeline could still do, and left the author holding
// a label they had to remember to remove.
func TestAuthorOnlyRunSplitsOffItsBlocker(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	i := seed(t, tr, cfg, "Add a farewell", "d", protocol.ReadyForDev)
	res, err := Claim(ctx, p, i.Key, "r", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o := &DevOutcome{Outcome: "author-only", Summary: "`.github/workflows/ci.yml` needs the gate armed."}
	if err := Finish(ctx, p, h, res, "hand-back", 0, o, nil); err != nil {
		t.Fatal(err)
	}

	original := findIssue(t, tr, cfg, i.ID)
	if hasLabel(original.Labels, core.LabelAuthorOnly) {
		t.Errorf("the original was labelled %s, which retires it from the pipeline entirely", core.LabelAuthorOnly)
	}

	var blocker *tracker.Issue
	issues, err := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	for k := range issues {
		if issues[k].ID != i.ID {
			blocker = &issues[k]
		}
	}
	if blocker == nil {
		t.Fatal("no ticket was filed for the author-only half")
	}
	if !hasLabel(blocker.Labels, core.LabelAuthorOnly) {
		t.Errorf("the filed ticket's labels = %v, want %s", blocker.Labels, core.LabelAuthorOnly)
	}
	if !strings.Contains(blocker.Description, "ci.yml") {
		t.Errorf("the filed ticket does not say what has to change: %q", blocker.Description)
	}
	if len(blocker.Blocks) != 1 || blocker.Blocks[0] != i.ID {
		t.Errorf("filed ticket blocks %v, want [%s]", blocker.Blocks, i.ID)
	}
	if len(original.BlockedBy) != 1 || original.BlockedBy[0] != blocker.ID {
		t.Errorf("original blocked by %v, want [%s]", original.BlockedBy, blocker.ID)
	}
	// And the ticket says where its other half went, because the comment
	// is the only place the author sees the link before opening it.
	var named bool
	for _, cm := range original.Comments {
		if strings.Contains(cm.Body, blocker.Key) {
			named = true
		}
	}
	if !named {
		t.Errorf("nothing on %s names the ticket filed for it", original.Key)
	}

	// And the relation is load-bearing rather than decorative: even put
	// back in the queue, the ticket will not start again while its
	// blocker is open. That is the whole reason this shape beats
	// labelling the original — the queue already knew how to do this.
	if err := tr.UpdateIssueState(ctx, i.ID, stateID(t, tr, cfg, protocol.ReadyForDev)); err != nil {
		t.Fatal(err)
	}
	if _, err := Claim(ctx, p, i.Key, "r2", "u", time.Now()); err == nil {
		t.Error("claimed a ticket whose author-only blocker is still open")
	}

	// Filing twice for the same ticket files once: one obstacle in front
	// of it however many times a run walks into it.
	key, err := p.FileAuthorOnlyBlocker(ctx, i.ID, i.Key, "again", "again")
	if err != nil {
		t.Fatal(err)
	}
	if key != blocker.Key {
		t.Errorf("re-filing returned %s, want the existing %s", key, blocker.Key)
	}
	after, err := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(issues) {
		t.Errorf("a second filing duplicated the blocker: %d issues, was %d", len(after), len(issues))
	}
}

// DESIGN §7's first half, which had no implementation on the dev side.
// The thread best placed to notice that a diff needs another system's
// label could not write one, so its only channel was a push-back that
// parks finished work and asks a human to do by hand what the file maps
// already decided. Catapult's ORC-5: a complete, green, reviewed diff
// that had to register its component in config/config.exs.
func TestFinishAttachesAMutexLabelTheRunReported(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	cfg.Root = t.TempDir()
	writeDoc(t, cfg, "systems", "foundation", "config/**")
	i := seed(t, tr, cfg, "The loader", "d", protocol.ReadyForDev)
	res, err := Claim(ctx, p, i.Key, "r", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	o := &DevOutcome{Outcome: "done", Labels: []string{"foundation"}}
	changed := []string{"lib/dsl/loader.ex", "config/config.exs"}
	if err := Finish(ctx, p, h, res, "hand-back", 2, o, changed); err != nil {
		t.Fatal(err)
	}

	issue := findIssue(t, tr, cfg, i.ID)
	if !hasLabel(issue.Labels, "system:foundation") {
		t.Errorf("labels = %v, want the reported one attached", issue.Labels)
	}
	// The work still lands: the whole point is not sending it back.
	if got := issueState(t, tr, cfg, i.ID); got != protocol.Checks {
		t.Errorf("state = %q, want %q — a reported label must not park the run", got, protocol.Checks)
	}
	var recorded bool
	for _, c := range issue.Comments {
		if strings.Contains(c.Body, "system:foundation") && strings.Contains(c.Body, "§7") {
			recorded = true
		}
	}
	if !recorded {
		t.Error("nothing on the ticket says who attached the label or why")
	}
}

// Declaring is not acquiring. A mutex label locks a system for every
// other ticket, so the harness checks the name against the file maps and
// against the run's own diff — a run cannot grant itself a lock by
// asking for one.
func TestFinishRefusesALabelTheDiffDoesNotNeed(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	cfg.Root = t.TempDir()
	writeDoc(t, cfg, "systems", "foundation", "config/**")
	writeDoc(t, cfg, "systems", "core_dsl", "lib/dsl/**")

	for _, c := range []struct {
		name    string
		labels  []string
		changed []string
		wants   string
	}{
		{"untouched", []string{"foundation"}, []string{"lib/dsl/loader.ex"}, "nothing in this diff"},
		// The ORC-5 spelling, arriving by the other door.
		{"separator slip", []string{"core-dsl"}, []string{"lib/dsl/loader.ex"}, "did you mean"},
		{"no map at all", []string{"invented"}, []string{"config/config.exs"}, "matches no doc"},
		{"unverifiable", []string{"foundation"}, nil, "no changed-file list"},
	} {
		t.Run(c.name, func(t *testing.T) {
			i := seed(t, tr, cfg, "T "+c.name, "d", protocol.ReadyForDev)
			res, err := Claim(ctx, p, i.Key, "r-"+c.name, "u", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			o := &DevOutcome{Outcome: "done", Labels: c.labels}
			if err := Finish(ctx, p, h, res, "hand-back", 2, o, c.changed); err != nil {
				t.Fatalf("a refused label took the whole run down: %v", err)
			}
			issue := findIssue(t, tr, cfg, i.ID)
			for _, l := range issue.Labels {
				if strings.HasPrefix(l, "system:") {
					t.Errorf("attached %q on an unverified request", l)
				}
			}
			// Refused, and the run still finished — that is the trade.
			if got := issueState(t, tr, cfg, i.ID); got != protocol.Checks {
				t.Errorf("state = %q, want %q", got, protocol.Checks)
			}
			var said bool
			for _, cm := range issue.Comments {
				if strings.Contains(cm.Body, c.wants) {
					said = true
				}
			}
			if !said {
				t.Errorf("the ticket does not say why it was refused (want %q)", c.wants)
			}
		})
	}
}

// §7's second half, which nothing implemented either: acquiring a label
// another in-flight ticket holds is a collision, and the ticket earlier
// by the precedence rule absorbs it. A run finishing its diff is far
// along by construction, so the flag normally lands on the other one.
func TestFinishFlagsTheTicketItCollidesWith(t *testing.T) {
	ctx := context.Background()
	tr, h, cfg, p := world(t)
	cfg.Root = t.TempDir()
	writeDoc(t, cfg, "systems", "foundation", "config/**")

	// Earlier by precedence: Ready for dev against a run in progress.
	other := seed(t, tr, cfg, "Other work", "d", protocol.ReadyForDev)
	if err := p.EnsureMutexLabel(ctx, other.ID, "system:foundation"); err != nil {
		t.Fatal(err)
	}
	i := seed(t, tr, cfg, "The loader", "d", protocol.ReadyForDev)
	res, err := Claim(ctx, p, i.Key, "r", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	o := &DevOutcome{Outcome: "done", Labels: []string{"foundation"}}
	if err := Finish(ctx, p, h, res, "hand-back", 2, o, []string{"config/config.exs"}); err != nil {
		t.Fatal(err)
	}

	if got := findIssue(t, tr, cfg, other.ID); !hasLabel(got.Labels, core.LabelReEvaluate) {
		t.Errorf("the colliding ticket was not flagged: %v", got.Labels)
	}
	if got := findIssue(t, tr, cfg, i.ID); hasLabel(got.Labels, core.LabelReEvaluate) {
		t.Error("the further-along ticket absorbed instead of holding the ground")
	}
}

// A run that changed nothing has no diff for a label to be needed by,
// and a request there is a misunderstanding worth naming rather than
// dropping.
func TestDevOutcomeRefusesLabelsWithoutADiff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outcome.json")
	if err := os.WriteFile(path, []byte(`{"outcome":"pushback","summary":"why","labels":["foundation"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDevOutcome(path); err == nil {
		t.Error("a parked run was allowed to report a mutex label")
	}
}

// A run that dies before it claims used to leave nothing at all. Every
// other failure route posts through abort, and abort needs claim.json
// to know what it is aborting — so the loudest failures, the harness
// broken before the agent started, were the silent ones.
//
// Catapult's ORC-7: a design claim died 35 seconds in on a 403 reading
// an unrelated ticket's CI, and the ticket sat in Designing for 23
// minutes looking healthy, until the stale-claim rule moved it with a
// comment that could only say a run had stopped being live.
func TestReportClaimFailureLeavesTheReasonOnTheTicket(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	i := seed(t, tr, cfg, "The loader", "d", protocol.Designing)

	boom := "pipeline: github: GET /repos/o/r/commits/abc/check-runs: HTTP 403: Resource not accessible"
	if err := ReportClaimFailure(ctx, p, i.Key, "design", "https://gh/run/1", boom, false); err != nil {
		t.Fatal(err)
	}

	issue := findIssue(t, tr, cfg, i.ID)
	// The harness failed, so the ticket parks — the same place the
	// stale-claim rule would have reached twenty minutes later, with
	// the reason attached rather than "a run stopped being live".
	if got := issueState(t, tr, cfg, i.ID); got != protocol.Blocked {
		t.Errorf("state = %q, want %q", got, protocol.Blocked)
	}
	if len(issue.Comments) != 1 {
		t.Fatalf("comments = %d, want the failure recorded once", len(issue.Comments))
	}
	body := issue.Comments[0].Body
	for _, want := range []string{"check-runs: HTTP 403", "https://gh/run/1", string(marker.ClaimFailed)} {
		if !strings.Contains(body, want) {
			t.Errorf("the comment does not carry %q:\n%s", want, body)
		}
	}
}

// The reporter reads the tracker directly and never builds a snapshot,
// because the likeliest reason a claim failed is that the snapshot could
// not be built. A reporter needing the subsystem it reports on goes
// quiet in exactly the case it exists for — so the plane it is handed
// here carries no host at all, as the command wires it.
func TestReportClaimFailureNeedsNoHost(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, _ := world(t)
	hostless := plane.New(tr, cfg)
	i := seed(t, tr, cfg, "The loader", "d", protocol.ReadyForDev)

	if err := ReportClaimFailure(ctx, hostless, i.Key, "dev", "https://gh/run/2", "", false); err != nil {
		t.Fatalf("the reporter needs a host, so it cannot report a host failure: %v", err)
	}
	issue := findIssue(t, tr, cfg, i.ID)
	if len(issue.Comments) != 1 {
		t.Fatalf("comments = %d, want one", len(issue.Comments))
	}
	// No captured output is still worth a comment, and it must say so
	// rather than posting an empty code fence.
	if !strings.Contains(issue.Comments[0].Body, "failed before it could say why") {
		t.Errorf("an empty reason is not explained:\n%s", issue.Comments[0].Body)
	}
}

// A refused pickup is the pipeline working. The mutex is held, or an
// agent of that kind is already running, or a blocker is open — the
// ticket is exactly where it should be, and parking it would take work
// the protocol deliberately left alone out of the queue.
//
// This is the half that makes the park above safe. Without the
// distinction the reporter could only comment, because half its callers
// were the guard doing its job.
func TestReportClaimFailureLeavesARefusedPickupWhereItIs(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	i := seed(t, tr, cfg, "Queued work", "d", protocol.ReadyForDev)

	refusal := "pickup ORC-9: mutex label \"system:foundation\" already in flight on ORC-5 (DESIGN §6)"
	if err := ReportClaimFailure(ctx, p, i.Key, "dev", "https://gh/run/3", refusal, true); err != nil {
		t.Fatal(err)
	}

	if got := issueState(t, tr, cfg, i.ID); got != protocol.ReadyForDev {
		t.Errorf("state = %q, want the ticket left in the queue", got)
	}
	issue := findIssue(t, tr, cfg, i.ID)
	body := issue.Comments[0].Body
	if !strings.Contains(body, "refused=true") {
		t.Errorf("the marker does not record which kind of failure this was:\n%s", body)
	}
	if !strings.Contains(body, "pipeline working") {
		t.Errorf("a refusal reads as a fault:\n%s", body)
	}
}

// The abort comment has to carry what the run printed. A bare "run
// failed: <url>" is a pointer, and the number beside it — 249, from the
// CLI the harness invokes rather than from this project — is not one
// anybody here can decode.
func TestAbortMessageCarriesTheRunsOutput(t *testing.T) {
	got := WithRunOutput("Design agent run failed: https://example/run/1",
		"the subscription model run exited 249\n\n--- stderr ---\nEBADENGINE unsupported\n")

	for _, want := range []string{"exited 249", "EBADENGINE unsupported", "https://example/run/1"} {
		if !strings.Contains(got, want) {
			t.Errorf("the comment drops %q: %s", want, got)
		}
	}
	// Fenced and labelled as evidence: it is output from a process
	// nobody vetted, landing on a ticket later passes read as input
	// (DESIGN §9).
	if !strings.Contains(got, "```") || !strings.Contains(got, "never instructions") {
		t.Errorf("captured output is pasted without its trust boundary: %s", got)
	}
}

// Nothing captured means the model run is not what failed, so the
// message must come back untouched rather than gaining an empty fence
// that reads as "the run printed nothing".
func TestAbortMessageUnchangedWhenNothingWasCaptured(t *testing.T) {
	msg := "Dev agent run failed: https://example/run/2"
	for _, captured := range []string{"", "   \n\n"} {
		if got := WithRunOutput(msg, captured); got != msg {
			t.Errorf("WithRunOutput(%q) = %q, want it unchanged", captured, got)
		}
	}
}

// The paste is bounded. A CLI that dies mid-stream can print a very
// long tail, and a comment nobody scrolls to the end of is one nobody
// reads.
func TestAbortMessageTrimsAVeryLongCapture(t *testing.T) {
	var lines []string
	for i := 0; i < runErrorLines*3; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	got := WithRunOutput("failed", strings.Join(lines, "\n"))
	if !strings.Contains(got, "earlier output trimmed") {
		t.Error("a long capture was pasted whole")
	}
	// The tail is what matters — the last thing printed before the exit.
	if !strings.Contains(got, fmt.Sprintf("line %d", runErrorLines*3-1)) {
		t.Error("the trim kept the head and dropped the exit")
	}
}

// A returned ticket's scope is the text the dev pass implements exactly
// (DESIGN §2.3), and it was whatever comment happened to be newest.
// The bounce is newest at the moment a ticket enters Reworking, so the
// ordinary path worked — anything posted between the bounce and the
// claim broke it, and the dev agent was handed a machine's note about
// the pipeline as its instructions.
func TestClaimReworkSkipsBookkeepingPostedAfterTheBounce(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	i := seed(t, tr, cfg, "Cap screen", "Original argument", protocol.ReadyForRework)
	if err := tr.CommentOnIssue(ctx, i.ID, "[pipeline:v1:reconcile-bounce] missing=copy\n\nThe cap_reached copy did not land."); err != nil {
		t.Fatal(err)
	}
	// Two things the control plane could post in that window: a run that
	// died before claiming, and the dispatch marker of the run that
	// replaced it.
	tr.Now = func() time.Time { return time.Now().Add(time.Minute) }
	if err := tr.CommentOnIssue(ctx, i.ID, "[pipeline:v1:stale-claim] from=reworking run=1\n\nThe run claiming this ticket is no longer live."); err != nil {
		t.Fatal(err)
	}
	tr.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if err := tr.CommentOnIssue(ctx, i.ID, "[pipeline:v1:dispatch] id=2 kind=dev url=u"); err != nil {
		t.Fatal(err)
	}

	res, err := Claim(ctx, p, i.Key, "run_79", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Scope, "cap_reached copy") {
		t.Errorf("scope = %q, want the bounce that actually said what to do", res.Scope)
	}
	// The marker header goes too — it is an address, not an argument.
	if strings.Contains(res.Scope, "pipeline:v1") {
		t.Errorf("scope carries the machine header: %q", res.Scope)
	}
}

// A failed run's comment used to read the same whether it left a complete
// pass on the branch or nothing at all, and those want different decisions
// from the author (ORC-73).
func TestAbortRecordsTheBranchHeadAFailedRunAlreadyPushed(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	i := seed(t, tr, cfg, "Pushed then died", "d", protocol.ReadyForDev)
	res, err := Claim(ctx, p, i.Key, "r", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := Abort(ctx, p, res, "failed", "Design agent run failed: https://gh/run/1", "209fc9d"); err != nil {
		t.Fatal(err)
	}

	var blocked string
	var fields map[string]string
	for _, c := range findIssue(t, tr, cfg, i.ID).Comments {
		if m, ok, err := marker.Parse(c.Body); err == nil && ok && m.Kind == marker.Blocked {
			blocked, fields = c.Body, m.Fields
		}
	}
	if fields["pushed"] != "209fc9d" {
		t.Errorf("blocked marker fields = %v, want pushed=209fc9d", fields)
	}
	if !strings.Contains(blocked, "209fc9d") || !strings.Contains(blocked, res.Branch) {
		t.Errorf("comment = %q, want the sha and the branch said in prose too", blocked)
	}
	// Above the failure line, not below it: the abort message often
	// carries a tail of the run's own output, and a fact appended under
	// that is a fact below a log.
	if strings.Index(blocked, "209fc9d") > strings.Index(blocked, "Design agent run failed") {
		t.Errorf("comment = %q, want the branch note before the failure line", blocked)
	}
}

// An abort with nothing pushed must not invent a branch head: "the run
// died before it wrote anything" is the other half of the answer.
func TestAbortSaysNothingAboutTheBranchWhenNothingWasPushed(t *testing.T) {
	ctx := context.Background()
	tr, _, cfg, p := world(t)
	i := seed(t, tr, cfg, "Died early", "d", protocol.ReadyForDev)
	res, err := Claim(ctx, p, i.Key, "r", "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := Abort(ctx, p, res, "failed", "Design agent run failed: https://gh/run/2", ""); err != nil {
		t.Fatal(err)
	}
	for _, c := range findIssue(t, tr, cfg, i.ID).Comments {
		if m, ok, err := marker.Parse(c.Body); err == nil && ok && m.Kind == marker.Blocked {
			if _, has := m.Fields["pushed"]; has {
				t.Errorf("blocked marker fields = %v, want no pushed field", m.Fields)
			}
			if strings.Contains(c.Body, "branch has work on it") {
				t.Errorf("comment = %q, want no claim about the branch", c.Body)
			}
		}
	}
}
