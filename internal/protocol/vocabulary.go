package protocol

// The closed vocabularies an agent may emit, in one place.
//
// Each of these was previously a bare `switch` in the file that enforced
// it, a list in a role prompt, prose in DESIGN, and — for proposal kinds
// — a fourth copy restated inside the test that was supposed to hold the
// first three together. That fourth copy is how the guard went stale: it
// was written as `{"debt", "design", "harness"}` and never updated when
// `bug` was added, so removing `bug` from the prompt's schema left the
// test green while no boundary pass could file one.
//
// So: the switches read these, the tests read these, and DESIGN §13's
// outcome table is asserted against these. A value added here and
// nowhere else fails CI naming the document it is missing from, which is
// the opposite of what happened before.
//
// What is checked and what is not, stated plainly: the **value** column
// of §13's table is held to these sets in both directions, and the
// **label** column is held for proposal kinds, which is the one mapping
// that is already a lookup rather than control flow. The state and
// marker columns are documentation — accurate when written, not derived.
// Encoding them would mean rewriting five switches into data-driven
// dispatch, and those switches validate arguments and call helpers as
// well as mapping, so the rewrite would risk more than the drift it
// prevents.
// The two collision verdicts live here rather than beside the reconcile
// pass that reads them, for the reason the whole file moved: the package
// that maps a proposal kind to a label is imported by the plane, and the
// plane cannot import the package that runs the agents without a cycle.
// Protocol is the floor everything already stands on.
const (
	CollisionHolds = "holds"
	CollisionBites = "bites"
)

var (
	// ProposalKinds are what a boundary pass may file into Triage
	// (DESIGN §10). `bug` was refused here until the never-bugs rule was
	// reversed; see §10 step 9.
	ProposalKinds = []string{"debt", "design", "harness", "bug"}

	// AbortReasons are the ways a run may end without finishing its
	// scope (DESIGN §12). `failed` is the only one that takes no label:
	// it means the harness broke, which is not a fact about the ticket.
	AbortReasons = []string{"pushback", "failed", "needs-setup", "author-only", "scope-satisfied"}

	// DesignOutcomes are what a design pass reports (DESIGN §2.7, §7).
	DesignOutcomes = []string{"artifacts", "decisionless", "clear", "demote"}

	// ReconcileOutcomes are the three verdicts (DESIGN §11).
	ReconcileOutcomes = []string{"pass", "fail", "cannot-tell"}

	// CollisionVerdicts answer whether a re-evaluate collision
	// invalidates the work in flight (DESIGN §7).
	CollisionVerdicts = []string{CollisionHolds, CollisionBites}
)

// ProposalLabels maps a proposal kind to the label it is filed under.
//
// A map rather than a switch because this one is pure: kind in, label
// out, no validation and no side effect. That makes it the one column of
// §13's table a test can hold to the code, so it is worth the shape.
//
// Three of the four are not the identity, which is the reason to write
// them down somewhere a reader will look: `debt` files as `tech-debt`
// and `design` as `design-inbox`, and somebody assuming otherwise is
// right about `harness` and `bug` and wrong about the rest.
var ProposalLabels = map[string]string{
	"debt":    "tech-debt",
	"design":  "design-inbox",
	"harness": "harness",
	"bug":     "bug",
}

// Known reports whether v is in a vocabulary.
func Known(vocab []string, v string) bool {
	for _, k := range vocab {
		if k == v {
			return true
		}
	}
	return false
}
