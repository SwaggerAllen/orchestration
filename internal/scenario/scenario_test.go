package scenario

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/retro"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

// world stands up a disposable project with the protocol's states and
// labels already provisioned, as `pipeline setup` would leave it.
func world(t *testing.T) (*tracker.Memory, *config.Config) {
	t.Helper()
	tr := tracker.NewMemory()
	cfg := config.Sample()
	cfg.Disposable = true
	ctx := context.Background()
	for _, ps := range protocol.AllStates {
		if _, err := tr.CreateState(ctx, cfg.Tracker.TeamID, tracker.NewState{Name: cfg.StateName(ps), Category: protocol.Categories[ps], Color: protocol.Colors[ps]}); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range protocol.Labels {
		if _, err := tr.CreateLabel(ctx, cfg.Tracker.TeamID, l); err != nil {
			t.Fatal(err)
		}
	}
	return tr, cfg
}

func basic() *Scenario {
	return &Scenario{
		Name:       "basic",
		Milestones: []Milestone{{Name: "M1", SortOrder: 1}},
		Tickets: []Ticket{{
			Ref: "a", Title: "Do a thing", Milestone: "M1",
			Labels: []string{"frontend"}, Priority: 2, State: protocol.Todo,
		}},
		Expect: Expect{FinalStates: map[string]protocol.State{"a": protocol.Done}},
	}
}

// The guard is the whole safety story for a command that archives every
// ticket in a project, so it is tested before anything it protects.
func TestResetRefusesAProjectThatIsNotDisposable(t *testing.T) {
	tr, cfg := world(t)
	cfg.Disposable = false

	_, err := Reset(context.Background(), tr, cfg, cfg.Tracker.ProjectID, t.TempDir(), &strings.Builder{})
	if err == nil {
		t.Fatal("reset ran against a config that never opted in")
	}
	var notDisposable ErrNotDisposable
	if !asErr(err, &notDisposable) {
		t.Fatalf("want ErrNotDisposable so the CLI can explain itself, got %T: %v", err, err)
	}
}

// Two keys, because either one alone is a mistake someone makes: a
// config pointed at the wrong project, or a command typed against the
// wrong config.
func TestResetRefusesAMismatchedConfirmation(t *testing.T) {
	tr, cfg := world(t)
	if err := seedOne(t, tr, cfg); err != nil {
		t.Fatal(err)
	}
	_, err := Reset(context.Background(), tr, cfg, "some-other-project", t.TempDir(), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("want a refusal naming the mismatch, got %v", err)
	}
	// And nothing was touched on the way to refusing.
	issues, _ := tr.ListIssues(context.Background(), cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if len(issues) != 1 {
		t.Errorf("a refused reset archived %d ticket(s)", 1-len(issues))
	}
}

func TestSeedThenResetReturnsTheProjectToEmpty(t *testing.T) {
	ctx := context.Background()
	tr, cfg := world(t)
	var log strings.Builder

	seeded, err := Seed(ctx, tr, cfg, basic(), &log)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded.Keys) != 1 || seeded.Keys["a"] == "" {
		t.Fatalf("seeded = %+v", seeded)
	}
	issues, _ := tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if len(issues) != 1 {
		t.Fatalf("seeded %d issues, want 1", len(issues))
	}
	if issues[0].Milestone != "M1" || issues[0].Priority != 2 {
		t.Errorf("seeded issue lost its milestone or priority: %+v", issues[0])
	}

	res, err := Reset(ctx, tr, cfg, cfg.Tracker.ProjectID, t.TempDir(), &log)
	if err != nil {
		t.Fatal(err)
	}
	if res.Archived != 1 {
		t.Errorf("reset archived %d, want 1", res.Archived)
	}
	issues, _ = tr.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if len(issues) != 0 {
		t.Errorf("%d issue(s) survived the reset", len(issues))
	}
}

// Milestones are reused rather than recreated, because their order is
// what "the next milestone" means (DESIGN §10) — recreating them every
// run would reshuffle the thing the boundary flow reads.
func TestSeedReusesMilestonesAcrossRuns(t *testing.T) {
	ctx := context.Background()
	tr, cfg := world(t)
	var log strings.Builder

	for i := 0; i < 2; i++ {
		if _, err := Seed(ctx, tr, cfg, basic(), &log); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
		if _, err := Reset(ctx, tr, cfg, cfg.Tracker.ProjectID, t.TempDir(), &log); err != nil {
			t.Fatal(err)
		}
	}
	ms, _ := tr.ListMilestones(ctx, cfg.Tracker.ProjectID)
	if len(ms) != 1 {
		t.Errorf("two runs left %d milestones, want 1 reused", len(ms))
	}
}

func TestCheckReportsEveryFailureNotJustTheFirst(t *testing.T) {
	ctx := context.Background()
	tr, cfg := world(t)
	s := basic()
	s.Tickets = append(s.Tickets, Ticket{Ref: "b", Title: "Another", Milestone: "M1", State: protocol.Todo})
	s.Expect.FinalStates["b"] = protocol.Done
	s.Expect.Files = []string{"screens/nope.md"}

	seeded, err := Seed(ctx, tr, cfg, s, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	// Nothing ran, so both tickets are still in Todo and the file is absent.
	failures, err := Check(ctx, tr, cfg, s, seeded, func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 3 {
		t.Fatalf("got %d failures, want 3 (two states and a file):\n%v", len(failures), failures)
	}
}

func TestCheckPassesWhenTheWorldMatches(t *testing.T) {
	ctx := context.Background()
	tr, cfg := world(t)
	s := basic()
	s.Expect.Markers = map[string][]string{"a": {"dispatch"}}
	s.Expect.AbsentMarkers = map[string][]string{"a": {"stale-claim"}}

	seeded, err := Seed(ctx, tr, cfg, s, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	id := seeded.IDs["a"]
	if err := tr.CommentOnIssue(ctx, id, "[pipeline:v1:dispatch] run=1"); err != nil {
		t.Fatal(err)
	}
	if err := tr.CommentOnIssue(ctx, id, "a human said something here"); err != nil {
		t.Fatal(err)
	}
	done := stateID(t, tr, cfg, protocol.Done)
	if err := tr.UpdateIssueState(ctx, id, done); err != nil {
		t.Fatal(err)
	}

	failures, err := Check(ctx, tr, cfg, s, seeded, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 0 {
		t.Errorf("expected a clean run, got %v", failures)
	}
}

// An archived ticket has left the listing, and the boundary pass
// archives Done work — so the message has to distinguish that from a
// ticket that never existed.
func TestCheckExplainsAMissingTicket(t *testing.T) {
	ctx := context.Background()
	tr, cfg := world(t)
	s := basic()
	seeded, err := Seed(ctx, tr, cfg, s, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Reset(ctx, tr, cfg, cfg.Tracker.ProjectID, t.TempDir(), &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	failures, err := Check(ctx, tr, cfg, s, seeded, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || !strings.Contains(failures[0].Got, "archived") {
		t.Errorf("want a failure naming archival as the likely cause, got %v", failures)
	}
}

func TestValidateRejectsFixturesThatProveLessThanTheyClaim(t *testing.T) {
	cases := map[string]func(*Scenario){
		"a ref in expect that no ticket defines": func(s *Scenario) {
			s.Expect.FinalStates = map[string]protocol.State{"typo": protocol.Done}
		},
		"a ticket seeded into a pipeline-owned state": func(s *Scenario) {
			s.Tickets[0].State = protocol.Reconciling
		},
		"a milestone the scenario never declares": func(s *Scenario) {
			s.Tickets[0].Milestone = "M9"
		},
		"two tickets sharing a ref": func(s *Scenario) {
			s.Tickets = append(s.Tickets, s.Tickets[0])
		},
		"no expectations at all": func(s *Scenario) {
			s.Expect = Expect{}
		},
		"no tickets at all": func(s *Scenario) {
			s.Tickets = nil
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := basic()
			mutate(s)
			if err := s.Validate(); err == nil {
				t.Error("validation accepted it")
			}
		})
	}
}

func TestShippedScenariosAreValid(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "scenarios", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no scenarios found — the harness ships none")
	}
	for _, p := range paths {
		if _, err := Load(p); err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
		}
	}
}

func seedOne(t *testing.T, tr *tracker.Memory, cfg *config.Config) error {
	t.Helper()
	_, err := Seed(context.Background(), tr, cfg, basic(), &strings.Builder{})
	return err
}

func stateID(t *testing.T, tr *tracker.Memory, cfg *config.Config, ps protocol.State) string {
	t.Helper()
	states, err := tr.ListStates(context.Background(), cfg.Tracker.TeamID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		if s.Name == cfg.StateName(ps) {
			return s.ID
		}
	}
	t.Fatalf("no state for %q", ps)
	return ""
}

func asErr[T error](err error, target *T) bool {
	if e, ok := err.(T); ok {
		*target = e
		return true
	}
	return false
}

// The repo half of a rehearsal reset has to know which commits the last
// run landed, and this is the only moment that answer exists: archiving
// drops an issue out of Linear's listings, so a later pass cannot ask.
// Collected from the ticket's own merged marker rather than guessed
// later from commit subjects or branch names.
func TestResetReportsWhatTheTicketsMergedBeforeArchivingThem(t *testing.T) {
	ctx := context.Background()
	tr, cfg := world(t)

	i, err := tr.CreateIssue(ctx, tracker.NewIssue{
		TeamID: cfg.Tracker.TeamID, ProjectID: cfg.Tracker.ProjectID,
		Title: "Landed one", StateID: doneState(t, tr, cfg),
	})
	if err != nil {
		t.Fatal(err)
	}
	m := marker.Marker{Kind: marker.Merged, Fields: map[string]string{"sha": "abc123def456", "pr": "6"}}
	if err := tr.CommentOnIssue(ctx, i.ID, m.Comment("Reconciled and merged.")); err != nil {
		t.Fatal(err)
	}
	// A ticket that never merged contributes nothing — most of them.
	if _, err := tr.CreateIssue(ctx, tracker.NewIssue{
		TeamID: cfg.Tracker.TeamID, ProjectID: cfg.Tracker.ProjectID,
		Title: "Never landed", StateID: doneState(t, tr, cfg),
	}); err != nil {
		t.Fatal(err)
	}

	res, err := Reset(ctx, tr, cfg, cfg.Tracker.ProjectID, t.TempDir(), &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Archived != 2 {
		t.Errorf("archived = %d, want 2", res.Archived)
	}
	if len(res.Merges) != 1 {
		t.Fatalf("merges = %+v, want exactly the one that landed", res.Merges)
	}
	if res.Merges[0].SHA != "abc123def456" || res.Merges[0].Key != i.Key || res.Merges[0].PR != "6" {
		t.Errorf("merge = %+v, want the marker's sha, the ticket's key and its PR", res.Merges[0])
	}
}

// The reset that measured green and did nothing.
//
// A milestone boundary archives the milestone's Done tickets (DESIGN §10
// step 6), and it runs before any reset does. Reset learns what to
// revert from `merged` markers on the tickets it archives — so after a
// boundary there are no tickets, the merge list came out empty, the repo
// half printed "the last rehearsal merged nothing" and main kept the
// rehearsal's commits. Nothing anywhere reported a failure.
//
// The retro note the archive step writes is the durable copy, and this
// is the whole point of reading it: the tracker is empty and the answer
// still exists.
func TestResetAfterABoundaryArchiveStillFindsWhatToRevert(t *testing.T) {
	ctx := context.Background()
	tr, cfg := world(t)

	// The project as a boundary leaves it: no tickets at all.
	repo := t.TempDir()
	writeRetroNote(t, repo, "rehearsal-1.md", "Rehearsal 1", []retro.Entry{
		{Key: "ORC-23", Title: "Add a farewell to the home screen",
			SHAs: []string{"0655929c405d57bc77b341c476267e45472e3985"}},
		{Key: "ORC-9", Title: "Decide the greeting copy"},
	})

	res, err := Reset(ctx, tr, cfg, cfg.Tracker.ProjectID, repo, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Archived != 0 {
		t.Fatalf("archived = %d, want 0 — the boundary already did it", res.Archived)
	}
	if len(res.Merges) != 1 || res.Merges[0].SHA != "0655929c405d57bc77b341c476267e45472e3985" {
		t.Fatalf("merges = %+v, want the commit the boundary archived out of reach", res.Merges)
	}
	if res.Merges[0].Key != "ORC-23" {
		t.Errorf("merge lost its ticket key: %+v", res.Merges[0])
	}
	if res.FromNotes != 1 {
		t.Errorf("fromNotes = %d, want 1 — the run summary is how a human sees this working", res.FromNotes)
	}
}

// The two sources union rather than either winning, the same way a
// resumed boundary unions the findings it can still collect with the
// ones its archive step carried. A rehearsal stopped partway through a
// boundary is exactly this: some tickets archived into the note, the
// rest still live and still carrying their markers.
func TestResetUnionsLiveTicketsWithTheRetroNotes(t *testing.T) {
	ctx := context.Background()
	tr, cfg := world(t)

	live, err := tr.CreateIssue(ctx, tracker.NewIssue{
		TeamID: cfg.Tracker.TeamID, ProjectID: cfg.Tracker.ProjectID,
		Title: "Still open", StateID: doneState(t, tr, cfg),
	})
	if err != nil {
		t.Fatal(err)
	}
	m := marker.Marker{Kind: marker.Merged, Fields: map[string]string{"sha": "aaaa111", "pr": "20"}}
	if err := tr.CommentOnIssue(ctx, live.ID, m.Comment("Reconciled and merged.")); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	writeRetroNote(t, repo, "rehearsal-1.md", "Rehearsal 1", []retro.Entry{
		{Key: "ORC-23", Title: "Archived", SHAs: []string{"bbbb222"}},
		// The same ticket the note already recorded — a boundary that
		// archived it, then a resume that re-listed it before the
		// archive took. One revert, not two.
		{Key: live.Key, Title: "Still open", SHAs: []string{"aaaa111"}},
	})
	// An older milestone's note. Its commits were reverted rehearsals
	// ago, but a note is never reverted away, so they come back every
	// run — harmless, because the revert loop skips a commit already
	// reverted or no longer on main.
	writeRetroNote(t, repo, "m-zero.md", "M: zero", []retro.Entry{
		{Key: "ORC-1", Title: "Ancient", SHAs: []string{"cccc333"}},
	})

	res, err := Reset(ctx, tr, cfg, cfg.Tracker.ProjectID, repo, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, mg := range res.Merges {
		got = append(got, mg.SHA)
	}
	// Each sha once. Their order among themselves is deliberately not
	// asserted: the revert step re-orders the whole list against
	// `git rev-list`, because neither tracker order nor a note's
	// key-sorted lines is landing order, and landing order is what
	// reverts cleanly.
	if len(got) != 3 {
		t.Fatalf("merges = %v, want three, each once", got)
	}
	sorted := append([]string(nil), got...)
	sort.Strings(sorted)
	if want := []string{"aaaa111", "bbbb222", "cccc333"}; !reflect.DeepEqual(sorted, want) {
		t.Fatalf("merges = %v, want %v", sorted, want)
	}
	// The live ticket won the dedupe, so its PR number survived.
	if res.Merges[0].PR != "20" {
		t.Errorf("merge = %+v, want the live ticket's marker to win the dedupe", res.Merges[0])
	}
	if res.FromNotes != 2 {
		t.Errorf("fromNotes = %d, want 2", res.FromNotes)
	}
}

// A reset with no project checkout cannot read the notes, and a reset
// that cannot read the notes reverts some of the last rehearsal and
// reports a clean run — which is the bug, not a degraded mode of it.
func TestResetRefusesWithoutAProjectCheckout(t *testing.T) {
	tr, cfg := world(t)
	_, err := Reset(context.Background(), tr, cfg, cfg.Tracker.ProjectID, "", &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "retro notes") {
		t.Fatalf("want a refusal naming the notes it could not read, got %v", err)
	}
}

// A checkout with no notes at all is a project that has never reached a
// boundary, not a misconfiguration — the common case for the first few
// rehearsals, and it must not fail.
func TestResetAcceptsACheckoutWithNoNotesYet(t *testing.T) {
	tr, cfg := world(t)
	res, err := Reset(context.Background(), tr, cfg, cfg.Tracker.ProjectID, t.TempDir(), &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Merges) != 0 {
		t.Errorf("merges = %+v, want none", res.Merges)
	}
}

func writeRetroNote(t *testing.T, repo, name, milestone string, entries []retro.Entry) {
	t.Helper()
	dir := filepath.Join(repo, retro.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(retro.Render(milestone, entries)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A rehearsal that merged nothing must report an empty list rather than
// an error: the repo step tells "this run landed nothing" from "the
// tracker step never ran", and it can only do that if empty is a legal
// answer.
func TestResetOnAProjectThatMergedNothingReportsNoMerges(t *testing.T) {
	ctx := context.Background()
	tr, cfg := world(t)
	res, err := Reset(ctx, tr, cfg, cfg.Tracker.ProjectID, t.TempDir(), &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Merges) != 0 {
		t.Errorf("merges = %+v, want none", res.Merges)
	}
}

func doneState(t *testing.T, tr tracker.Tracker, cfg *config.Config) string {
	t.Helper()
	states, err := tr.ListStates(context.Background(), cfg.Tracker.TeamID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		if s.Name == cfg.StateName(protocol.Done) {
			return s.ID
		}
	}
	t.Fatal("no done state")
	return ""
}

// A rehearsal that merged nothing must still write a JSON array. The
// repo half of the reset reads this file with `jq '.[].sha'`, and a nil
// slice marshals to `null`, which jq cannot iterate — so the reset died
// on "Cannot iterate over null" in exactly the case it handles
// explicitly two lines later ("the last rehearsal merged nothing").
// Asserted on the marshalled bytes, because the shape the workflow sees
// is the only shape that matters here.
func TestResetWritesAnArrayWhenNothingMerged(t *testing.T) {
	tr, cfg := world(t)
	if err := seedOne(t, tr, cfg); err != nil {
		t.Fatal(err)
	}
	res, err := Reset(context.Background(), tr, cfg, cfg.Tracker.ProjectID, t.TempDir(), &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Merges) != 0 {
		t.Fatalf("the seed merged nothing but the reset found %v", res.Merges)
	}
	raw, err := json.Marshal(res.Merges)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "[]" {
		t.Errorf("merges marshalled to %s; jq '.[]' cannot iterate that", raw)
	}
}
