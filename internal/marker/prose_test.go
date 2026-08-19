package marker

import "testing"

// The control plane's notes to itself must not reach a prompt. A ticket
// that fails repeatedly grows its own: each failed run appends a
// dispatch marker and a blocked marker that the next claim inlines.
func TestProseDropsTheControlPlanesOwnBookkeeping(t *testing.T) {
	for _, body := range []string{
		Marker{Kind: Dispatch, Fields: map[string]string{"id": "32290021120", "kind": "design"}}.Comment(""),
		Marker{Kind: Base, Fields: map[string]string{"sha": "f17a2cc"}}.Comment("Design was drawn against `f17a2cc`."),
		Marker{Kind: Preview, Fields: map[string]string{"url": "https://x.pages.dev"}}.Comment("Storybook preview for this pass: https://x.pages.dev"),
		Marker{Kind: StaleClaim, Fields: map[string]string{"from": "designing"}}.Comment("The design run claiming this ticket is no longer live."),
	} {
		if got, worth := Prose(body); worth {
			t.Errorf("kept a bookkeeping comment: %q", got)
		}
	}
}

// The half that must survive. These carry the argument a returning pass
// is supposed to act on, under a header that happens to be machine
// readable — dropping them by kind would delete the instructions.
func TestProseKeepsTheArgumentUnderTheHeader(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{Marker{Kind: ReconcileBounce, Fields: map[string]string{}}.Comment("The cap_reached state renders but nothing sets it."), "The cap_reached state renders but nothing sets it."},
		{Marker{Kind: Blocked, Fields: map[string]string{"from": "in_progress", "pushback": "1"}}.Comment("The sketch needs a table that does not exist."), "The sketch needs a table that does not exist."},
		{Marker{Kind: DecisionlessPass, Fields: map[string]string{}}.Comment("No screens; index work inside search."), "No screens; index work inside search."},
		{"Just a human comment.", "Just a human comment."},
	}
	for _, c := range cases {
		got, worth := Prose(c.body)
		if !worth || got != c.want {
			t.Errorf("Prose(%q) = %q, %v; want %q, true", c.body, got, worth, c.want)
		}
	}
}

// One kind, two meanings. A push-back's `blocked` carries the argument;
// a failed run's `blocked` carries a URL and, since the harness started
// capturing what a run printed, up to forty lines of a CLI's death
// rattle — the previous run's crash handed to the next run as context.
func TestProseSplitsBlockedByItsFlavor(t *testing.T) {
	failed := Marker{Kind: Blocked, Fields: map[string]string{"from": "designing"}}.
		Comment("Design agent run failed: https://example/run/1\n\n```\n/usr/bin/env: Argument list too long\n```")
	if got, worth := Prose(failed); worth {
		t.Errorf("a failed run's crash reached the next prompt: %q", got)
	}
	for _, flavor := range []string{"pushback", "setup", "author-only", "scope-satisfied"} {
		body := Marker{Kind: Blocked, Fields: map[string]string{"from": "in_progress", flavor: "1"}}.Comment("The argument.")
		if _, worth := Prose(body); !worth {
			t.Errorf("%s: the argument that parked the ticket was dropped", flavor)
		}
	}
}

// A display decision, not a parse: showing a model one odd line beats
// hiding a comment somebody wrote.
func TestProseKeepsAMalformedMarker(t *testing.T) {
	if got, worth := Prose("[pipeline:v9:dispatch] id=1\n\nprose"); !worth || got == "" {
		t.Errorf("a future-version marker was dropped entirely: %q", got)
	}
}
