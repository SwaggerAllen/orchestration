package marker

import (
	"maps"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	cases := []Marker{
		{Kind: Dispatch, Fields: map[string]string{"id": "run-4321", "state": "in_progress"}},
		{Kind: CIRed, Fields: map[string]string{"run": "https://github.com/x/y/actions/runs/1", "attempt": "2"}},
		{Kind: BoundaryStep, Fields: map[string]string{"step": "debt-scan"}},
		{Kind: TriageProposal, Fields: map[string]string{"dedupe": "M3/skipped-test lib/foo_test.exs"}},
		{Kind: Revert, Fields: map[string]string{"rule": "screen-mutex", "from": "Ready for dev", "to": "Design review"}},
		{Kind: DecisionlessPass, Fields: map[string]string{}},
		{Kind: StaleClaim, Fields: map[string]string{"note": `quotes " and \ and
newline and	tab`}},
		{Kind: ReconcileBounce, Fields: map[string]string{"empty": ""}},
	}
	for _, want := range cases {
		t.Run(string(want.Kind), func(t *testing.T) {
			line := want.Format()
			got, ok, err := Parse(line)
			if err != nil || !ok {
				t.Fatalf("Parse(%q) = ok=%v err=%v", line, ok, err)
			}
			if got.Kind != want.Kind || !maps.Equal(got.Fields, want.Fields) {
				t.Errorf("round trip: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestFormatIsDeterministic(t *testing.T) {
	m := Marker{Kind: CIRed, Fields: map[string]string{"b": "2", "a": "1", "c": "3"}}
	want := `[pipeline:v1:ci-red] a=1 b=2 c=3`
	if got := m.Format(); got != want {
		t.Errorf("Format() = %q, want %q", got, want)
	}
}

func TestCommentKeepsProseOffTheMarkerLine(t *testing.T) {
	m := Marker{Kind: CIRed, Fields: map[string]string{"attempt": "1"}}
	body := m.Comment("mix test failed: 3 failures.\nSee the run for logs.")
	got, ok, err := Parse(body)
	if err != nil || !ok {
		t.Fatalf("Parse = ok=%v err=%v", ok, err)
	}
	if got.Fields["attempt"] != "1" {
		t.Errorf("fields = %v", got.Fields)
	}
}

func TestParseHumanProseIsNotAMarker(t *testing.T) {
	for _, body := range []string{
		"Looks good, shipping it.",
		"",
		"pipeline: not a marker",
	} {
		_, ok, err := Parse(body)
		if ok || err != nil {
			t.Errorf("Parse(%q) = ok=%v err=%v, want ok=false err=nil", body, ok, err)
		}
	}
}

func TestParseUnknownKindIsAccepted(t *testing.T) {
	got, ok, err := Parse("[pipeline:v1:future-kind] a=1")
	if err != nil || !ok {
		t.Fatalf("Parse = ok=%v err=%v", ok, err)
	}
	if got.Kind != "future-kind" {
		t.Errorf("kind = %q", got.Kind)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"wrong version":     "[pipeline:v0:ci-red] a=1",
		"unterminated head": "[pipeline:v1:ci-red a=1",
		"no kind":           "[pipeline:v1] a=1",
		"bare field":        "[pipeline:v1:ci-red] justakey",
		"duplicate key":     "[pipeline:v1:ci-red] a=1 a=2",
		"unterminated q":    `[pipeline:v1:ci-red] a="oops`,
		"bad escape":        `[pipeline:v1:ci-red] a="\x"`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := Parse(body)
			if err == nil {
				t.Errorf("Parse(%q): want error", body)
			}
		})
	}
}
