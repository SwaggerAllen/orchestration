// Package protocol holds the canonical vocabulary of the pipeline: the state
// set, each state's tracker category, and the label set, per DESIGN.md §3 and
// §8. Projects map canonical states to tracker state names in their config;
// nothing outside the config ever assumes a tracker spelling, so a project
// renaming a state is a config edit rather than a code change.
package protocol

import "strings"

// State is a canonical pipeline state.
type State string

const (
	Backlog        State = "backlog"
	Todo           State = "todo"
	ReadyForDesign State = "ready_for_design"
	Designing      State = "designing"
	DesignReview   State = "design_review"
	// ReadyForRedesign is the design queue a pass is sent back to with a
	// scope comment — a record-review decline or a demote (DESIGN §3,
	// §4) — the mirror of ReadyForRework. Ready for design is where a
	// fresh ticket waits; a bounced design waited there too until this
	// state existed, and on the board a bounce looked like a new ticket.
	ReadyForRedesign State = "ready_for_redesign"
	ReadyForDev      State = "ready_for_dev"
	InProgress       State = "in_progress"
	Checks           State = "checks"
	Reconciling      State = "reconciling"
	ReadyForRework   State = "ready_for_rework"
	Reworking        State = "reworking"
	Merged           State = "merged"
	BoundaryReview   State = "boundary_review"
	Blocked          State = "blocked"
	Done             State = "done"
	Canceled         State = "canceled"
)

// AllStates is the full canonical set in rough pipeline order. The exact
// "furthest along" ordering the precedence rule needs (DESIGN §7) is defined
// with the pure core in M1; this slice exists so config validation and setup
// can enumerate the set.
var AllStates = []State{
	Backlog,
	Todo,
	ReadyForDesign,
	Designing,
	DesignReview,
	ReadyForRedesign,
	ReadyForDev,
	InProgress,
	Checks,
	Reconciling,
	ReadyForRework,
	Reworking,
	Merged,
	BoundaryReview,
	Blocked,
	Done,
	Canceled,
}

// Category is the tracker's built-in state type. Linear requires exactly one
// per workflow state and uses it for its own grouping and automation; the
// in-memory fake enforces the same requirement so setup is tested against
// the real constraint.
type Category string

const (
	CategoryBacklog   Category = "backlog"
	CategoryUnstarted Category = "unstarted"
	CategoryStarted   Category = "started"
	CategoryCompleted Category = "completed"
	CategoryCanceled  Category = "canceled"
	// CategoryTriage is Linear's intake state type. Setup never creates
	// one — Linear manages it when the team enables Triage — but the
	// boundary agent files proposals into it when it exists (DESIGN §10).
	CategoryTriage Category = "triage"
	// CategoryDuplicate is Linear's own type for its built-in Duplicate
	// state, and it is a type of its own rather than a flavour of
	// `canceled`. Setup never creates one; it is declared so the plane
	// can read a ticket someone marked Duplicate in the UI, and so the
	// in-memory tracker can be given the category Linear actually
	// returns. Without it the fake rejected the real value, which is how
	// a regression test for this state came to seed a different one.
	CategoryDuplicate Category = "duplicate"
)

// Categories assigns each canonical state its tracker category. Queue states
// (`Ready for dev`, `Ready for rework`) are "unstarted" because nobody has
// the ball (DESIGN §3); everything agent- or author-held is "started".
var Categories = map[State]Category{
	Backlog:          CategoryBacklog,
	Todo:             CategoryUnstarted,
	ReadyForDesign:   CategoryUnstarted,
	Designing:        CategoryStarted,
	DesignReview:     CategoryStarted,
	ReadyForRedesign: CategoryUnstarted,
	ReadyForDev:      CategoryUnstarted,
	InProgress:       CategoryStarted,
	Checks:           CategoryStarted,
	Reconciling:      CategoryStarted,
	ReadyForRework:   CategoryUnstarted,
	Reworking:        CategoryStarted,
	Merged:           CategoryStarted,
	BoundaryReview:   CategoryStarted,
	Blocked:          CategoryStarted,
	Done:             CategoryCompleted,
	Canceled:         CategoryCanceled,
}

// Colors are the hex colors the pipeline's states are created with.
//
// Keyed by state rather than by category, because nine of these states
// share the `started` category — a category-keyed palette paints most of
// the board one colour and the board then tells you nothing.
//
// The families answer "what is happening to this, and is any of it
// mine?" at a glance:
//
//	grey    nothing is happening — queued at one end, finished at the
//	        other. Done is grey with the other terminal states on
//	        purpose: finished work is out of mind, so green is spent on
//	        work in flight instead.
//	violet, cyan   a design or reconcile agent is working
//	green   the dev agent is producing something
//	yellow  machinery is verifying or shipping it — CI, then deploy
//	orange  waiting on the author
//	red     stuck, and the author's to unstick
//
// Nothing reads these back; they exist so the author can see the queue
// without reading it.
var Colors = map[State]string{
	Backlog:          "#bec2c8", // grey — not scheduled
	Todo:             "#e2e2e2", // light grey — queued
	ReadyForDesign:   "#e2e2e2", // light grey — queued, like the other queues
	Designing:        "#9b8fd4", // violet — design agent
	DesignReview:     "#f2994a", // orange — YOUR sign-off
	ReadyForRedesign: "#e2e2e2", // light grey — queued; the name is the signal, not the colour
	ReadyForDev:      "#e2e2e2", // light grey — queued
	InProgress:       "#4cb782", // green — dev agent building
	Checks:           "#f2c94c", // yellow — CI verifying
	Reconciling:      "#26b5ce", // cyan — reconcile agent
	ReadyForRework:   "#e2e2e2", // light grey — queued
	Reworking:        "#4cb782", // green — dev agent again
	Merged:           "#f2c94c", // yellow — waiting on the deploy
	BoundaryReview:   "#f2994a", // orange — YOUR pass
	Blocked:          "#eb5757", // red — needs you
	Done:             "#95a2b3", // dark grey — terminal, out of mind
	Canceled:         "#95a2b3", // dark grey — terminal
}

// Labels is the fixed label set every project team carries (DESIGN §8).
// screen:<name> labels are created per screen as work discovers them, so
// they are a prefix rather than a member of this set.
var Labels = []string{
	"frontend",
	"backend",
	"tech-debt",
	"bug",
	"design-inbox",
	"re-evaluate",
	"needs-review",
	"needs-setup",
	"scope-satisfied",
	"pushback",
	"author-only",
	"resync",
	"prerequisite",
	"harness",
	"milestone-boundary",
}

// The mutex label prefixes, one per record directory (DESIGN §6):
// screens cover design artifacts, systems the structural units the
// sketch declares, dsl the grammar contract a project's bundles are
// written against. One mutex rule spans all of them.
const (
	ScreenLabelPrefix = "screen:"
	SystemLabelPrefix = "system:"
	DslLabelPrefix    = "dsl:"
)

// RecordKind is one directory of record documents (DESIGN §4): docs a
// design pass writes, each carrying rule ids, a reasons sibling and a
// file map in its front matter, each claimed by a mutex label.
//
// Three fields that used to be derived from the directory name, and
// each derivation broke on the first kind whose name did not fit the
// pun:
//
//   - Cite was Dir minus a trailing "s" (systems -> system). "docs/dsl"
//     has no such form.
//   - Dir was assumed to be a single path segment, so a changed path
//     was split on its first "/" to find it. Under that split every doc
//     in a nested directory is silently not a record doc — the check
//     passes by looking where the answer cannot be.
//   - Exclusive says a path may have at most one owner in this kind, so
//     that the mutex it feeds is unambiguous. It is true of systems and
//     was written as "no two *system* docs", with screens exempt because
//     a screen and a system describe the same path from two sides. dsl
//     is exempt for a nearer reason: its docs are the grammar of one
//     file set, and bundle.md, chain.md and workflow.md map overlapping
//     parts of bundles/** by construction.
type RecordKind struct {
	Dir         string
	Cite        string
	LabelPrefix string
	Exclusive   bool
}

// RecordKinds is the canonical list. A project missing one of these
// directories simply has no docs of that kind: every loader treats a
// missing directory as no docs, so a project that has not written a
// grammar contract is not failed over one.
var RecordKinds = []RecordKind{
	{Dir: "systems", Cite: "system", LabelPrefix: SystemLabelPrefix, Exclusive: true},
	{Dir: "screens", Cite: "screen", LabelPrefix: ScreenLabelPrefix},
	{Dir: "docs/dsl", Cite: "dsl", LabelPrefix: DslLabelPrefix},
}

// RecordKindByCite finds the kind a citation prefix names ("system").
func RecordKindByCite(cite string) (RecordKind, bool) {
	for _, k := range RecordKinds {
		if k.Cite == cite {
			return k, true
		}
	}
	return RecordKind{}, false
}

// RecordKindByDir finds the kind a directory names ("docs/dsl").
func RecordKindByDir(dir string) (RecordKind, bool) {
	for _, k := range RecordKinds {
		if k.Dir == dir {
			return k, true
		}
	}
	return RecordKind{}, false
}

// RecordKindForPath splits a repo-relative path into the record kind
// holding it and the rest of the path. The longest matching Dir wins, so
// a kind nested inside another's directory resolves to the nested one
// rather than to whichever is listed first.
func RecordKindForPath(p string) (kind RecordKind, rest string, ok bool) {
	for _, k := range RecordKinds {
		if !strings.HasPrefix(p, k.Dir+"/") {
			continue
		}
		if ok && len(k.Dir) <= len(kind.Dir) {
			continue
		}
		kind, rest, ok = k, p[len(k.Dir)+1:], true
	}
	return kind, rest, ok
}

// IsMutexLabel reports whether a label is one of the mutex labels — any
// kind's, since one mutex rule spans them all (DESIGN §6).
func IsMutexLabel(l string) bool {
	for _, k := range RecordKinds {
		if len(l) > len(k.LabelPrefix) && strings.HasPrefix(l, k.LabelPrefix) {
			return true
		}
	}
	return false
}

// AuthorOnlyPaths are the paths no agent can land a change to, whatever
// the ticket says. A ticket whose work is in one of them carries the
// author-only label (DESIGN §5, §8) and is never dispatched.
//
// Protocol rather than per-project config, because neither constraint is
// a project's choice:
//
//   - .github/workflows/** — the agent's push token carries no `workflow`
//     scope on any GitHub repository, and the rejected push takes the
//     whole run down with it, hand-back included. Nothing about a
//     project changes that.
//   - pipeline.config.json — it declares the gates, the states and the
//     ownership the run is being judged against. An agent editing it
//     mid-ticket is an agent changing the rules it is scored by.
//
// Matched with filemap.Match, so `**` spans directories.
var AuthorOnlyPaths = []string{
	".github/workflows/**",
	"pipeline.config.json",
}
