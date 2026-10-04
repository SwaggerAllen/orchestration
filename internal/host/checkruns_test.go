package host

import (
	"reflect"
	"strings"
	"testing"
)

// CheckRuns is off Host on purpose, and this is the assertion that makes
// the separation real rather than a comment.
//
// A fine-grained PAT cannot be granted the check-runs API at all. Host is
// what the plane holds, and the sweep and every agent claim build a full
// snapshot through it under AGENT_GITHUB_TOKEN — so a check-runs method
// on Host is reachable from that path, and reachable is one refactor away
// from called. Catapult's ORC-7 is what that costs: a design claim dead
// 35 seconds in on a 403 reading a different ticket's check runs, sitting
// in Designing until the stale-claim grace expired 23 minutes later.
//
// Asserted over the method set rather than by reading the source, because
// the failure is adding a method and not the words describing one.
func TestHostPortExcludesTheCheckRunsAPI(t *testing.T) {
	hostType := reflect.TypeOf((*Host)(nil)).Elem()
	checkRunsType := reflect.TypeOf((*CheckRuns)(nil)).Elem()

	if checkRunsType.NumMethod() == 0 {
		t.Fatal("CheckRuns declares no methods; this test would pass vacuously")
	}
	for i := 0; i < checkRunsType.NumMethod(); i++ {
		name := checkRunsType.Method(i).Name
		if _, found := hostType.MethodByName(name); found {
			t.Errorf("Host declares %s — a fine-grained token cannot be granted check-runs, and the snapshot runs under one", name)
		}
	}
	// The mirror: a method whose name merely mentions checks is not the
	// hazard, so this names the two rather than pattern-matching. If
	// CheckRuns grows a method, the loop above covers it and this stays
	// as the statement of what the rule is about.
	for _, name := range []string{"CheckRunsFor", "CreateCheckRun"} {
		if _, found := hostType.MethodByName(name); found {
			t.Errorf("Host declares %s", name)
		}
		if _, found := checkRunsType.MethodByName(name); !found {
			t.Errorf("CheckRuns no longer declares %s — has it moved onto Host?", name)
		}
	}
	// ChecksFor is Host's own CI verdict read and is *not* this API: it
	// goes through the Actions API precisely because check-runs is
	// ungrantable. Naming it here stops a later pass reading the rule as
	// "Host may not look at CI".
	if _, found := hostType.MethodByName("ChecksFor"); !found {
		t.Error("Host no longer declares ChecksFor; the Actions-API reader is what makes the exclusion above affordable")
	}
}

// The fake is the one both the judge command's tests and any future
// caller use, so its filtering has to match the adapter's.
func TestMemoryCheckRunsFiltersBySHAAndPrefix(t *testing.T) {
	m := NewMemoryCheckRuns()
	for _, cr := range []CheckRun{
		{SHA: "a", Name: "manual/one", Conclusion: CheckRunSuccess},
		{SHA: "a", Name: "gates", Conclusion: CheckRunSuccess},
		{SHA: "b", Name: "manual/one", Conclusion: CheckRunFailure},
	} {
		if err := m.CreateCheckRun(nil, cr); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.CheckRunsFor(nil, "a", "manual/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "manual/one" || got[0].Conclusion != CheckRunSuccess {
		t.Errorf("got %+v", got)
	}
}

func TestMemoryCheckRunsFailuresStandInForThe403(t *testing.T) {
	m := NewMemoryCheckRuns()
	m.FailRead, m.FailCreate = true, true
	if _, err := m.CheckRunsFor(nil, "a", "manual/"); err == nil {
		t.Error("read did not fail")
	}
	if err := m.CreateCheckRun(nil, CheckRun{SHA: "a", Name: "x"}); err == nil {
		t.Error("create did not fail")
	} else if !strings.Contains(err.Error(), "x") {
		t.Errorf("error %q does not name the run", err)
	}
}
