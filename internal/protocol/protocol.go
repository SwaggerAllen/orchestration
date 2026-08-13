// Package protocol holds the canonical vocabulary of the pipeline: the state
// set, each state's tracker category, and the label set, per DESIGN.md §3 and
// §8. Projects map canonical states to tracker state names in their config;
// nothing outside the config ever assumes a tracker spelling, so a project
// renaming a state is a config edit rather than a code change.
package protocol

// State is a canonical pipeline state.
type State string

const (
	Backlog        State = "backlog"
	Todo           State = "todo"
	Designing      State = "designing"
	DesignReview   State = "design_review"
	ReadyForDev    State = "ready_for_dev"
	InProgress     State = "in_progress"
	Checks         State = "checks"
	Reconciling    State = "reconciling"
	ReadyForRework State = "ready_for_rework"
	Reworking      State = "reworking"
	Merged         State = "merged"
	BoundaryReview State = "boundary_review"
	Blocked        State = "blocked"
	Done           State = "done"
	Canceled       State = "canceled"
)

// AllStates is the full canonical set in rough pipeline order. The exact
// "furthest along" ordering the precedence rule needs (DESIGN §7) is defined
// with the pure core in M1; this slice exists so config validation and setup
// can enumerate the set.
var AllStates = []State{
	Backlog,
	Todo,
	Designing,
	DesignReview,
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
)

// Categories assigns each canonical state its tracker category. Queue states
// (`Ready for dev`, `Ready for rework`) are "unstarted" because nobody has
// the ball (DESIGN §3); everything agent- or author-held is "started".
var Categories = map[State]Category{
	Backlog:        CategoryBacklog,
	Todo:           CategoryUnstarted,
	Designing:      CategoryStarted,
	DesignReview:   CategoryStarted,
	ReadyForDev:    CategoryUnstarted,
	InProgress:     CategoryStarted,
	Checks:         CategoryStarted,
	Reconciling:    CategoryStarted,
	ReadyForRework: CategoryUnstarted,
	Reworking:      CategoryStarted,
	Merged:         CategoryStarted,
	BoundaryReview: CategoryStarted,
	Blocked:        CategoryStarted,
	Done:           CategoryCompleted,
	Canceled:       CategoryCanceled,
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
	Backlog:        "#bec2c8", // grey — not scheduled
	Todo:           "#e2e2e2", // light grey — queued
	Designing:      "#9b8fd4", // violet — design agent
	DesignReview:   "#f2994a", // orange — YOUR sign-off
	ReadyForDev:    "#e2e2e2", // light grey — queued
	InProgress:     "#4cb782", // green — dev agent building
	Checks:         "#f2c94c", // yellow — CI verifying
	Reconciling:    "#26b5ce", // cyan — reconcile agent
	ReadyForRework: "#e2e2e2", // light grey — queued
	Reworking:      "#4cb782", // green — dev agent again
	Merged:         "#f2c94c", // yellow — waiting on the deploy
	BoundaryReview: "#f2994a", // orange — YOUR pass
	Blocked:        "#eb5757", // red — needs you
	Done:           "#95a2b3", // dark grey — terminal, out of mind
	Canceled:       "#95a2b3", // dark grey — terminal
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
	"milestone-boundary",
}

// ScreenLabelPrefix and SystemLabelPrefix mark the two kinds of mutex
// label (DESIGN §6): screens cover design artifacts, systems cover the
// structural units the sketch declares. One mutex rule spans both.
const (
	ScreenLabelPrefix = "screen:"
	SystemLabelPrefix = "system:"
)
