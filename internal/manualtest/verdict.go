package manualtest

import (
	"fmt"

	"github.com/SwaggerAllen/orchestration/internal/host"
)

// NamePrefix prefixes every manual test's check run name.
//
// The name is the identity a verdict is compared by (§8.3 rule 8), so it
// has to be stable across commits and derived from the test rather than
// from the run: two judge passes over one SHA have to produce the same
// name or the comparison finds nothing and reports agreement.
const NamePrefix = "manual/"

// CheckRunName is the check run a test's verdict is recorded as.
func CheckRunName(testID string) string { return NamePrefix + testID }

// Verdict is what one judge pass concluded about one test.
type Verdict struct {
	TestID string
	// SHA is the commit judged. Carried here rather than patched onto
	// the check run afterwards, because it is half the identity rule 8
	// compares by — "the same test on the same SHA" — and a Disagreement
	// that cannot name the commit is a finding nobody can go and look at.
	SHA string
	// Passed is the judge's own answer about the thing under test.
	Passed bool
	// Title and Summary are the judge's words, shown in the Checks tab.
	Title, Summary string
	// EvidenceURL points at the run whose artifacts hold the
	// screenshots, the trace, the transcript.
	EvidenceURL string
}

// Disagreement is §8.3 rule 8: this pass and an earlier one reached
// different verdicts about one test on one commit. It is a finding about
// the judge, not about the code.
type Disagreement struct {
	TestID string
	// SHA is the commit both verdicts were about.
	SHA string
	// Prior and Now are the two conclusions.
	Prior, Now host.CheckRunConclusion
	// PriorDetailsURL is the earlier run, so the two can be read
	// side by side.
	PriorDetailsURL string
}

func (d Disagreement) String() string {
	return fmt.Sprintf("%s on %s: this pass concluded %q on a commit already recorded as %q (%s)",
		d.TestID, d.SHA, d.Now, d.Prior, d.PriorDetailsURL)
}

// Record decides the check run to write for a verdict, given the verdicts
// already recorded for that test on that commit.
//
// Two rules, and the order matters because the second reads the first's
// output.
//
// **Rule 3: a pass with no evidence is a failure.** Not a pass with a
// warning — a failure, for the reason a probe must assert that it edited
// something: the dangerous outcome is not a judge that fails, it is one
// that never ran and printed `ok`. A judge session that produced no
// artifact is indistinguishable from one that did nothing, and the
// verdict has to be the same in both cases or the gate is decoration.
//
// **Rule 8: a verdict differing from one already recorded for this test
// on this commit is a finding about the judge**, recorded `neutral` and
// returned as a Disagreement. Neither success nor failure, because what
// it reports is that two runs disagreed rather than anything about the
// code: failure would spend the ticket's escalation budget on work that
// was never broken (§12's two), and success would bury it. It is never a
// re-run either — a third pass would be a third opinion, not a
// tie-break.
//
// Prior `neutral` runs are ignored when looking for the recorded verdict.
// A neutral is a finding about the judge, so comparing against one would
// report a disagreement on every later pass forever, and the first
// disagreement would poison the test's whole history on that commit.
//
// **Any disagreeing prior counts, not the newest one.** GitHub documents
// no ordering for the check-runs listing, and which prior is "the"
// recorded verdict would decide whether a finding is reported — so the
// question asked is "does any recorded verdict differ", which is
// order-independent. Two priors that disagree with each other are
// already a finding by the same test.
func Record(v Verdict, prior []host.CheckRun) (host.CheckRun, *Disagreement) {
	name := CheckRunName(v.TestID)
	out := host.CheckRun{
		SHA:        v.SHA,
		Name:       name,
		Title:      v.Title,
		Summary:    v.Summary,
		DetailsURL: v.EvidenceURL,
	}
	switch {
	case v.Passed && v.EvidenceURL == "":
		out.Conclusion = host.CheckRunFailure
		out.Title = "passed with no evidence, which is a failure"
		out.Summary = "The judge reported a pass and attached no artifact, so nothing distinguishes this " +
			"from a pass that never ran (ops-free-pipeline.md §8.3 rule 3). The judge's own words follow.\n\n" + v.Summary
	case v.Passed:
		out.Conclusion = host.CheckRunSuccess
	default:
		out.Conclusion = host.CheckRunFailure
	}

	for _, p := range prior {
		if p.Name != name || p.Conclusion == host.CheckRunNeutral {
			continue
		}
		if p.Conclusion == out.Conclusion {
			continue
		}
		d := &Disagreement{
			TestID: v.TestID, SHA: v.SHA, Prior: p.Conclusion, Now: out.Conclusion,
			PriorDetailsURL: p.DetailsURL,
		}
		return host.CheckRun{
			SHA:        v.SHA,
			Name:       name,
			Conclusion: host.CheckRunNeutral,
			Title:      "the judge disagreed with itself on this commit",
			Summary: d.String() + "\n\nThis is a finding about the judge rather than about the code, so it is " +
				"neither a pass nor a failure and does not count toward the two failures that block a ticket " +
				"(ops-free-pipeline.md §8.3 rule 8, DESIGN §12). Nothing is re-run: a third pass would be a " +
				"third opinion, not a tie-break.\n\nThis pass said:\n\n" + v.Summary,
			DetailsURL: v.EvidenceURL,
		}, d
	}
	return out, nil
}
