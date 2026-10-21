package manualtest

import (
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/host"
)

func pass(id, evidence string) Verdict {
	return Verdict{TestID: id, SHA: "sha1", Passed: true, Title: "it drained", Summary: "observed 0 jobs", EvidenceURL: evidence}
}

func fail(id, evidence string) Verdict {
	return Verdict{TestID: id, SHA: "sha1", Passed: false, Title: "it did not drain", Summary: "observed 2 jobs", EvidenceURL: evidence}
}

func priorRun(id string, c host.CheckRunConclusion, url string) host.CheckRun {
	return host.CheckRun{Name: CheckRunName(id), Conclusion: c, DetailsURL: url}
}

func TestRecordAPassWithEvidence(t *testing.T) {
	cr, d := Record(pass("queue-drains", "https://gh/run/1"), nil)
	if d != nil {
		t.Fatalf("disagreement = %v", d)
	}
	if cr.Name != "manual/queue-drains" || cr.Conclusion != host.CheckRunSuccess {
		t.Errorf("check run = %+v", cr)
	}
	if cr.DetailsURL != "https://gh/run/1" {
		t.Errorf("evidence link lost: %+v", cr)
	}
}

func TestRecordAFailure(t *testing.T) {
	cr, d := Record(fail("queue-drains", "https://gh/run/1"), nil)
	if d != nil || cr.Conclusion != host.CheckRunFailure {
		t.Errorf("cr = %+v, d = %v", cr, d)
	}
	if cr.Title != "it did not drain" || !strings.Contains(cr.Summary, "observed 2 jobs") {
		t.Errorf("the judge's own words were dropped: %+v", cr)
	}
}

// Rule 3, and the reason it is a failure rather than a warning: the
// dangerous outcome is not a judge that fails, it is one that never ran
// and printed ok. A pass with no artifact is indistinguishable from that.
func TestRecordAPassWithNoEvidenceIsAFailure(t *testing.T) {
	cr, d := Record(pass("queue-drains", ""), nil)
	if d != nil {
		t.Fatalf("disagreement = %v", d)
	}
	if cr.Conclusion != host.CheckRunFailure {
		t.Fatalf("conclusion = %q, want failure", cr.Conclusion)
	}
	// The reader has to be told why, or this looks like the test failing.
	if !strings.Contains(cr.Title, "no evidence") {
		t.Errorf("title = %q", cr.Title)
	}
	// And the judge's own words survive, so the reader can see what it
	// claimed to have observed.
	if !strings.Contains(cr.Summary, "observed 0 jobs") {
		t.Errorf("summary = %q", cr.Summary)
	}
}

// A failure with no evidence is already a failure, so rule 3 changes
// nothing — and must not relabel it as the evidence problem, because the
// reader's next step differs.
func TestRecordAFailureWithNoEvidenceKeepsItsOwnReason(t *testing.T) {
	cr, _ := Record(fail("queue-drains", ""), nil)
	if cr.Conclusion != host.CheckRunFailure {
		t.Fatalf("conclusion = %q", cr.Conclusion)
	}
	if strings.Contains(cr.Title, "no evidence") {
		t.Errorf("a failing test was relabelled as an evidence problem: %q", cr.Title)
	}
}

// Rule 8. Neither a pass nor a failure: failure would spend the ticket's
// escalation budget on work that was never broken, success would bury it.
func TestRecordADisagreementIsNeutralAndReported(t *testing.T) {
	cr, d := Record(fail("queue-drains", "https://gh/run/2"),
		[]host.CheckRun{priorRun("queue-drains", host.CheckRunSuccess, "https://gh/run/1")})
	if d == nil {
		t.Fatal("two different verdicts on one commit must be reported")
	}
	if cr.Conclusion != host.CheckRunNeutral {
		t.Errorf("conclusion = %q, want neutral", cr.Conclusion)
	}
	if d.Prior != host.CheckRunSuccess || d.Now != host.CheckRunFailure {
		t.Errorf("disagreement = %+v", d)
	}
	// Both runs have to be reachable from the finding, or nobody can
	// compare them.
	if d.PriorDetailsURL != "https://gh/run/1" || cr.DetailsURL != "https://gh/run/2" {
		t.Errorf("d = %+v, cr = %+v", d, cr)
	}
	// The comment says it does not count toward §12's two, because a
	// reader deciding whether to act needs that stated.
	if !strings.Contains(cr.Summary, "does not count toward") {
		t.Errorf("summary = %q", cr.Summary)
	}
	// And this pass's own reasoning survives. A finding that says two
	// runs disagreed without saying what either concluded is unusable
	// for the thing it exists for — diagnosing the judge — and dropping
	// it printed `ok` until this line existed.
	if !strings.Contains(cr.Summary, "observed 2 jobs") {
		t.Errorf("the judge's reasoning was dropped from the finding: %q", cr.Summary)
	}
	if !strings.Contains(cr.Summary, "success") || !strings.Contains(cr.Summary, "failure") {
		t.Errorf("the finding does not name both conclusions: %q", cr.Summary)
	}
}

func TestRecordAgreementWithAPriorIsNoFinding(t *testing.T) {
	cr, d := Record(pass("queue-drains", "https://gh/run/2"),
		[]host.CheckRun{priorRun("queue-drains", host.CheckRunSuccess, "https://gh/run/1")})
	if d != nil {
		t.Fatalf("agreement reported as a disagreement: %v", d)
	}
	if cr.Conclusion != host.CheckRunSuccess {
		t.Errorf("conclusion = %q", cr.Conclusion)
	}
}

// A neutral is a finding about the judge, not a verdict about the test.
// Comparing against one would report a disagreement on every later pass
// forever — the first flap would poison the test's whole history on that
// commit.
func TestRecordIgnoresAPriorNeutral(t *testing.T) {
	cr, d := Record(pass("queue-drains", "https://gh/run/3"),
		[]host.CheckRun{priorRun("queue-drains", host.CheckRunNeutral, "https://gh/run/2")})
	if d != nil {
		t.Fatalf("a prior neutral was read as a verdict: %v", d)
	}
	if cr.Conclusion != host.CheckRunSuccess {
		t.Errorf("conclusion = %q", cr.Conclusion)
	}
}

// Another test's verdict on the same commit is not this test's. The name
// is the identity, which is why it has to be derived from the test.
func TestRecordIgnoresAnotherTestsVerdict(t *testing.T) {
	cr, d := Record(pass("queue-drains", "https://gh/run/2"),
		[]host.CheckRun{priorRun("board-renders", host.CheckRunFailure, "https://gh/run/1")})
	if d != nil {
		t.Fatalf("another test's verdict was compared: %v", d)
	}
	if cr.Conclusion != host.CheckRunSuccess {
		t.Errorf("conclusion = %q", cr.Conclusion)
	}
}

// GitHub documents no ordering for the check-runs listing, so the
// question asked is "does any recorded verdict differ" rather than
// "does the newest". Listed with the agreeing one first, an
// order-dependent reading finds nothing.
func TestRecordFindsADisagreementWhateverTheOrder(t *testing.T) {
	prior := []host.CheckRun{
		priorRun("queue-drains", host.CheckRunSuccess, "https://gh/run/1"),
		priorRun("queue-drains", host.CheckRunFailure, "https://gh/run/2"),
	}
	for _, v := range []Verdict{pass("queue-drains", "https://gh/run/3"), fail("queue-drains", "https://gh/run/3")} {
		cr, d := Record(v, prior)
		if d == nil {
			t.Errorf("passed=%v: priors that disagree with each other are a finding whichever way this pass went", v.Passed)
		}
		if cr.Conclusion != host.CheckRunNeutral {
			t.Errorf("passed=%v: conclusion = %q", v.Passed, cr.Conclusion)
		}
	}
}

// Rule 3 runs before rule 8, and this is the case that shows it: an
// earlier pass with evidence, this one without. Both judged the thing the
// same, and the run that lost its evidence is a judge behaving
// differently on one input — which is exactly what rule 8 is for.
func TestRecordAPassThatLostItsEvidenceDisagreesWithAPriorPass(t *testing.T) {
	cr, d := Record(pass("queue-drains", ""),
		[]host.CheckRun{priorRun("queue-drains", host.CheckRunSuccess, "https://gh/run/1")})
	if d == nil {
		t.Fatal("a pass that produced no artifact where an earlier one did is a finding about the judge")
	}
	if cr.Conclusion != host.CheckRunNeutral {
		t.Errorf("conclusion = %q", cr.Conclusion)
	}
}

// Both the check run and the finding have to name the commit: the check
// run because that is what a verdict is attached to, and the finding
// because "the same test on the same SHA" is the identity rule 8 compares
// by, and a finding that cannot name the commit is one nobody can go and
// look at.
func TestRecordCarriesTheCommitOntoBothOutputs(t *testing.T) {
	cr, d := Record(fail("queue-drains", "https://gh/run/2"),
		[]host.CheckRun{priorRun("queue-drains", host.CheckRunSuccess, "https://gh/run/1")})
	if cr.SHA != "sha1" {
		t.Errorf("check run SHA = %q", cr.SHA)
	}
	if d == nil || d.SHA != "sha1" {
		t.Errorf("disagreement = %+v", d)
	}
	if !strings.Contains(d.String(), "sha1") {
		t.Errorf("the finding does not name the commit: %q", d.String())
	}
	// And on the agreeing path too, where there is no disagreement to
	// carry it.
	clean, _ := Record(pass("queue-drains", "https://gh/run/1"), nil)
	if clean.SHA != "sha1" {
		t.Errorf("check run SHA = %q on the clean path", clean.SHA)
	}
}

func TestCheckRunNameIsStableAndPrefixed(t *testing.T) {
	if got := CheckRunName("queue-drains"); got != "manual/queue-drains" {
		t.Errorf("name = %q", got)
	}
	if !strings.HasPrefix(CheckRunName("x"), NamePrefix) {
		t.Error("the name does not carry the prefix the reader queries by")
	}
}
