package scenario

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
	"github.com/SwaggerAllen/orchestration/internal/retro"
	"github.com/SwaggerAllen/orchestration/internal/tracker"
)

// ErrNotDisposable is returned by Reset when pointed at a config that
// does not declare itself disposable. It is a separate error type so
// the CLI can say something better than "failed".
type ErrNotDisposable struct{ ProjectID string }

func (e ErrNotDisposable) Error() string {
	return fmt.Sprintf(
		"refusing to reset project %s: its config does not set \"disposable\": true.\n"+
			"Reset archives every ticket in the project. Only a rehearsal project should carry that flag,\n"+
			"and no real project's config should ever gain it.", e.ProjectID)
}

// Guard is the two-key check on a destructive command. The config must
// declare the project disposable, and the caller must name the project
// id independently — so neither a config pointed at the wrong project
// nor a command typed against the wrong config is enough on its own.
func Guard(cfg *config.Config, confirmProjectID string) error {
	if !cfg.Disposable {
		return ErrNotDisposable{ProjectID: cfg.Tracker.ProjectID}
	}
	if confirmProjectID != cfg.Tracker.ProjectID {
		return fmt.Errorf(
			"refusing to reset: --confirm %q does not match the config's project %q",
			confirmProjectID, cfg.Tracker.ProjectID)
	}
	return nil
}

// Merged is one ticket's landed commit, read off its merged marker.
type Merged struct {
	Key string `json:"key"`
	SHA string `json:"sha"`
	PR  string `json:"pr,omitempty"`
}

// ResetResult is what a reset found and did. Merges is the list the
// repo half of a rehearsal reset needs.
type ResetResult struct {
	Archived int      `json:"archived"`
	Merges   []Merged `json:"merges"`
	// FromNotes is how many of Merges came from retro notes rather than
	// from live tickets. Reported because it is the difference between a
	// rehearsal that ended at a milestone boundary and one that did not,
	// and because the bug this fixes was silent: the run summary said
	// "merged nothing" and was believed.
	FromNotes int `json:"fromNotes"`
}

// ReadRetroNotes collects every retro note in a project checkout, in
// path order. A repo with no notes yet is not an error — no boundary has
// run there — but a repo that cannot be read is, because the difference
// between "no notes" and "wrong directory" is the whole value of the
// check.
func ReadRetroNotes(repoRoot string) ([]retro.Entry, error) {
	dir := filepath.Join(repoRoot, retro.Dir)
	names, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading retro notes: %w", err)
	}
	var out []retro.Entry
	for _, n := range names {
		if n.IsDir() || !strings.HasSuffix(n.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, n.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading retro note %s: %w", n.Name(), err)
		}
		out = append(out, retro.Parse(string(raw))...)
	}
	return out, nil
}

// Reset archives every issue in the project, returning it to empty, and
// reports the commits those issues merged.
//
// Archive rather than delete: Linear keeps archived issues recoverable,
// so a reset fired at the wrong moment costs a restore rather than the
// work. Milestones are left alone — they are reused by name across runs,
// which is what keeps "the next milestone" ordering stable (DESIGN §10).
//
// The merge list is collected here, before anything is archived, because
// for the live tickets this is the only moment it exists: an archived
// issue drops out of Linear's listings, so a later pass cannot ask what
// a previous rehearsal landed. It comes from each ticket's `merged`
// marker, which reconcile writes with the squash commit's sha — the
// harness's own record of what it merged, rather than a guess made later
// from commit subjects or branch names.
//
// repoRoot is a checkout of the project repo, and it is not optional: a
// milestone boundary archives the milestone's Done tickets before any
// reset runs (DESIGN §10 step 6), so a rehearsal that reached one has
// already lost its merge markers to the tracker. What it has instead is
// the retro note that step writes, which carries the same shas — read
// here and unioned with whatever tickets are still live. Measured on
// orchestration-dummy: ORC-1 and ORC-18 reverted, ORC-23 (PR #15) left
// on main by a reset that reported success, with `retro: Rehearsal 1`
// directly above it in the log.
func Reset(ctx context.Context, t tracker.Tracker, cfg *config.Config, confirmProjectID, repoRoot string, log io.Writer) (*ResetResult, error) {
	if err := Guard(cfg, confirmProjectID); err != nil {
		return nil, err
	}
	if repoRoot == "" {
		return nil, fmt.Errorf(
			"refusing to reset: no project checkout given, so the retro notes cannot be read.\n" +
				"Everything a past milestone boundary archived is recorded only there, and a reset that\n" +
				"skipped them would revert some of the last rehearsal and report a clean run.")
	}
	issues, err := t.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if err != nil {
		return nil, err
	}
	// Merges starts as an empty slice, not nil. A nil slice marshals to
	// `null`, and the repo half of the reset reads this file with
	// `jq '.[]'`, which cannot iterate null — so a rehearsal that merged
	// nothing died on its own reset with "Cannot iterate over null"
	// instead of printing "merged nothing" and carrying on. The empty
	// case is the one the revert loop was never run against.
	res := &ResetResult{Merges: []Merged{}}
	seen := map[string]bool{}
	add := func(m Merged, where string) {
		if m.SHA == "" || seen[m.SHA] {
			return
		}
		seen[m.SHA] = true
		res.Merges = append(res.Merges, m)
		fmt.Fprintf(log, "  merged   %s %s (%s)\n", m.Key, m.SHA, where)
	}
	for _, i := range issues {
		for _, m := range mergedMarkers(i) {
			m.Key = i.Key
			add(m, "ticket")
		}
	}
	// After the live tickets, so a ticket still carrying its marker wins
	// the dedupe and keeps its PR number. Notes from every past
	// milestone are read, not just this rehearsal's: a note is never
	// reverted away, so old shas come back every run. They cost nothing
	// — the revert loop skips a commit that is already reverted or no
	// longer on main — and singling out "the last one" would mean the
	// harness deciding which note was current, which it cannot know.
	notes, err := ReadRetroNotes(repoRoot)
	if err != nil {
		return nil, err
	}
	fromTickets := len(res.Merges)
	for _, e := range notes {
		for _, sha := range e.SHAs {
			add(Merged{Key: e.Key, SHA: sha}, "retro note")
		}
	}
	res.FromNotes = len(res.Merges) - fromTickets

	for _, i := range issues {
		fmt.Fprintf(log, "  archive %s %s\n", i.Key, i.Title)
		if err := t.ArchiveIssue(ctx, i.ID); err != nil {
			return nil, fmt.Errorf("archiving %s: %w", i.Key, err)
		}
	}
	res.Archived = len(issues)
	return res, nil
}

// mergedMarkers reads a ticket's merged markers, oldest first — the
// order the comments are in, which is the order the commits landed.
// More than one is possible in principle (a ticket merged, reverted by
// hand, and merged again), and all of them are reported: a reset that
// silently dropped the older one would leave half a ticket on main.
func mergedMarkers(i tracker.Issue) []Merged {
	var out []Merged
	for _, c := range i.Comments {
		m, ok, err := marker.Parse(c.Body)
		if err != nil || !ok || m.Kind != marker.Merged {
			continue
		}
		if sha := m.Fields["sha"]; sha != "" {
			out = append(out, Merged{SHA: sha, PR: m.Fields["pr"]})
		}
	}
	return out
}

// Seeded maps a scenario ref to the ticket the tracker created for it.
// Written out by Seed and read by Check, because the check phase runs
// in a later process and cannot re-derive which key belongs to which ref.
type Seeded struct {
	Scenario string            `json:"scenario"`
	Keys     map[string]string `json:"keys"` // ref -> ticket key
	IDs      map[string]string `json:"ids"`  // ref -> ticket id
}

// Seed creates the scenario's milestones and tickets. Milestones are
// created only if absent; tickets are always created, which is why Seed
// belongs after Reset rather than instead of it.
func Seed(ctx context.Context, t tracker.Tracker, cfg *config.Config, s *Scenario, log io.Writer) (*Seeded, error) {
	states, err := stateIDs(ctx, t, cfg)
	if err != nil {
		return nil, err
	}

	existing, err := t.ListMilestones(ctx, cfg.Tracker.ProjectID)
	if err != nil {
		return nil, err
	}
	milestoneID := map[string]string{}
	for _, m := range existing {
		milestoneID[m.Name] = m.ID
	}
	for _, m := range s.Milestones {
		if id, ok := milestoneID[m.Name]; ok {
			fmt.Fprintf(log, "  milestone %q already exists (%s)\n", m.Name, id)
			continue
		}
		created, err := t.CreateMilestone(ctx, cfg.Tracker.ProjectID, m.Name, m.SortOrder)
		if err != nil {
			return nil, fmt.Errorf("creating milestone %q: %w", m.Name, err)
		}
		fmt.Fprintf(log, "  created milestone %q\n", m.Name)
		milestoneID[m.Name] = created.ID
	}

	out := &Seeded{Scenario: s.Name, Keys: map[string]string{}, IDs: map[string]string{}}
	for _, tk := range s.Tickets {
		stateID, ok := states[tk.State]
		if !ok {
			return nil, fmt.Errorf("ticket %s: no tracker state for %q — run pipeline setup first", tk.Ref, tk.State)
		}
		issue, err := t.CreateIssue(ctx, tracker.NewIssue{
			TeamID:      cfg.Tracker.TeamID,
			ProjectID:   cfg.Tracker.ProjectID,
			MilestoneID: milestoneID[tk.Milestone],
			Title:       tk.Title,
			Description: tk.Description,
			StateID:     stateID,
			Labels:      tk.Labels,
		})
		if err != nil {
			return nil, fmt.Errorf("creating ticket %s: %w", tk.Ref, err)
		}
		if tk.Priority != 0 {
			if err := t.UpdateIssuePriority(ctx, issue.ID, tk.Priority); err != nil {
				return nil, fmt.Errorf("setting priority on %s: %w", issue.Key, err)
			}
		}
		fmt.Fprintf(log, "  created %s (%s) in %s\n", issue.Key, tk.Ref, tk.State)
		out.Keys[tk.Ref] = issue.Key
		out.IDs[tk.Ref] = issue.ID
	}
	return out, nil
}

// Failure is one unmet expectation.
type Failure struct {
	Ref  string `json:"ref,omitempty"`
	Key  string `json:"key,omitempty"`
	Want string `json:"want"`
	Got  string `json:"got"`
}

func (f Failure) String() string {
	who := f.Ref
	if f.Key != "" {
		who = fmt.Sprintf("%s (%s)", f.Ref, f.Key)
	}
	if who == "" {
		return fmt.Sprintf("want %s, got %s", f.Want, f.Got)
	}
	return fmt.Sprintf("%s: want %s, got %s", who, f.Want, f.Got)
}

// HasFile reports whether a repository path exists. Supplied by the
// caller so Check needs no host adapter: in the harness workflow the
// project is checked out on disk, and asking the filesystem is both
// simpler and more honest than asking an API about a branch.
type HasFile func(path string) bool

// Check compares the live project against the scenario's expectations
// and returns every failure rather than the first, because a rehearsal
// that reports one problem per run takes as many runs as it has
// problems.
func Check(ctx context.Context, t tracker.Tracker, cfg *config.Config, s *Scenario, seeded *Seeded, hasFile HasFile) ([]Failure, error) {
	issues, err := t.ListIssues(ctx, cfg.Tracker.TeamID, cfg.Tracker.ProjectID)
	if err != nil {
		return nil, err
	}
	byKey := map[string]tracker.Issue{}
	for _, i := range issues {
		byKey[i.Key] = i
	}
	stateName, err := stateNames(ctx, t, cfg)
	if err != nil {
		return nil, err
	}

	var failures []Failure
	fail := func(f Failure) { failures = append(failures, f) }

	for _, ref := range sortedKeys(s.Expect.FinalStates) {
		want := s.Expect.FinalStates[ref]
		key := seeded.Keys[ref]
		issue, ok := byKey[key]
		if !ok {
			// Archived is the ordinary way a ticket leaves the listing,
			// and the boundary pass archives Done work (DESIGN §10) — so
			// say which it is rather than guessing.
			fail(Failure{Ref: ref, Key: key, Want: fmt.Sprintf("state %s", want), Got: "not in the project (archived, or never created)"})
			continue
		}
		if got := stateName[issue.StateID]; got != want {
			fail(Failure{Ref: ref, Key: key, Want: fmt.Sprintf("state %s", want), Got: fmt.Sprintf("state %s", got)})
		}
	}

	for _, ref := range sortedKeys(s.Expect.Markers) {
		key := seeded.Keys[ref]
		present := markerKinds(byKey[key])
		for _, kind := range s.Expect.Markers[ref] {
			if !present[kind] {
				fail(Failure{Ref: ref, Key: key, Want: fmt.Sprintf("a %s marker", kind), Got: "no such marker on the ticket"})
			}
		}
	}
	for _, ref := range sortedKeys(s.Expect.AbsentMarkers) {
		key := seeded.Keys[ref]
		present := markerKinds(byKey[key])
		for _, kind := range s.Expect.AbsentMarkers[ref] {
			if present[kind] {
				fail(Failure{Ref: ref, Key: key, Want: fmt.Sprintf("no %s marker", kind), Got: "the ticket carries one"})
			}
		}
	}

	if hasFile != nil {
		for _, path := range s.Expect.Files {
			if !hasFile(path) {
				fail(Failure{Want: fmt.Sprintf("file %s", path), Got: "missing from the repository"})
			}
		}
	}
	return failures, nil
}

// markerKinds is the set of pipeline marker kinds on a ticket. A comment
// that fails to parse as a marker is prose, which is the common case and
// not an error.
func markerKinds(i tracker.Issue) map[string]bool {
	kinds := map[string]bool{}
	for _, c := range i.Comments {
		if m, ok, err := marker.Parse(c.Body); ok && err == nil {
			kinds[string(m.Kind)] = true
		}
	}
	return kinds
}

func stateIDs(ctx context.Context, t tracker.Tracker, cfg *config.Config) (map[protocol.State]string, error) {
	states, err := t.ListStates(ctx, cfg.Tracker.TeamID)
	if err != nil {
		return nil, err
	}
	byName := map[string]string{}
	for _, s := range states {
		byName[s.Name] = s.ID
	}
	out := map[protocol.State]string{}
	for _, ps := range protocol.AllStates {
		if id, ok := byName[cfg.StateName(ps)]; ok {
			out[ps] = id
		}
	}
	return out, nil
}

func stateNames(ctx context.Context, t tracker.Tracker, cfg *config.Config) (map[string]protocol.State, error) {
	ids, err := stateIDs(ctx, t, cfg)
	if err != nil {
		return nil, err
	}
	out := map[string]protocol.State{}
	for ps, id := range ids {
		out[id] = ps
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
