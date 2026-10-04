package main

import (
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// The four things the report has to carry per ticket, in a shape a
// reader can act on without opening the tracker to find out what a key
// refers to.
func TestPrintedOrderCarriesTitleLinkAndBothDirections(t *testing.T) {
	o := &core.Order{Layers: []core.Layer{{
		Name: core.LayerReady, Why: "nothing outstanding blocks them.",
		Tickets: []core.OrderedTicket{{
			Key: "ORC-7", Title: "Word the farewell", URL: "https://tracker.invalid/issue/ORC-7",
			State:     protocol.Todo,
			BlockedBy: []core.Neighbour{{Key: "ORC-3", Title: "Land the workflow edit", State: protocol.InProgress}},
			Blocks:    []core.Neighbour{{Key: "ORC-9", Title: "Wire the screen", State: protocol.Backlog}},
			ScopeSharedWith: []core.Neighbour{{
				Key: "ORC-4", Title: "Engine work", State: protocol.Checks, Label: "system:engine",
			}},
		}},
	}}}

	var b strings.Builder
	printOrder(&b, o, "M1")
	got := b.String()

	for _, want := range []string{
		"ORC-7", "Word the farewell", "https://tracker.invalid/issue/ORC-7",
		"blocked by:", "ORC-3", "Land the workflow edit",
		"blocks:", "ORC-9", "Wire the screen",
		"milestone M1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report is missing %q:\n%s", want, got)
		}
	}
	// The scope line must not read as a blocker, or the reader will treat
	// an available ticket as unavailable. Nothing refuses on a shared
	// scope (DESIGN §6), so the note says what it actually means: both
	// run, and their branches meet in a merge.
	if !strings.Contains(got, "both may run") {
		t.Errorf("the scope note reads as a blocker:\n%s", got)
	}
	// And it says the ordering is derived, so nobody pastes it somewhere
	// as a plan and acts on it a week later.
	if !strings.Contains(got, "nothing here is stored") {
		t.Errorf("the report does not say it is derived:\n%s", got)
	}
}

// The markdown form is what gets read when nobody is at a terminal, and
// the one thing it must do that the text form cannot is turn each key
// into something tappable — a report read on a phone whose keys are
// plain text sends the reader to a tracker search box, which is the
// errand it exists to spare them.
func TestTheMarkdownOrderLinksEveryKey(t *testing.T) {
	o := &core.Order{Layers: []core.Layer{
		{
			Name: core.LayerReady, Why: "nothing outstanding blocks them.",
			Tickets: []core.OrderedTicket{{
				Key: "ORC-7", Title: "Word the farewell", URL: "https://tracker.invalid/issue/ORC-7",
				State:     protocol.Todo,
				Milestone: "M2",
				Note:      "outside the current milestone",
				BlockedBy: []core.Neighbour{{Key: "ORC-3", Title: "Land the workflow edit", State: protocol.InProgress}},
				Blocks:    []core.Neighbour{{Key: "ORC-9", Title: "Wire the screen", State: protocol.Backlog}},
				ScopeSharedWith: []core.Neighbour{{
					Key: "ORC-4", Title: "Engine work", State: protocol.Checks, Label: "system:engine",
				}},
			}},
		},
		{Name: core.LayerRest, Why: "blocked by something itself blocked."},
	}}

	var b strings.Builder
	printOrderMarkdown(&b, o, "")
	got := b.String()

	if !strings.Contains(got, "[ORC-7 · Word the farewell](https://tracker.invalid/issue/ORC-7)") {
		t.Errorf("the key is not a link:\n%s", got)
	}
	for _, want := range []string{
		"### " + core.LayerReady, "### " + core.LayerRest, // every layer, including the empty one
		"blocked by `ORC-3` Land the workflow edit",
		"blocks `ORC-9` Wire the screen",
		"milestone M2", "note: outside the current milestone",
		"nothing here is stored",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report is missing %q:\n%s", want, got)
		}
	}
	// Same trap as the text form: a shared scope that reads as a blocker
	// makes an available ticket look unavailable.
	if !strings.Contains(got, "both may run") {
		t.Errorf("the scope note reads as a blocker:\n%s", got)
	}
	// An empty layer says so rather than vanishing — "no tickets are
	// ready" and "the report stopped early" must not look alike.
	if !strings.Contains(got, "_(none)_") {
		t.Errorf("an empty layer prints nothing at all:\n%s", got)
	}
}

// The decision has to be in both forms, above the layers, because it is
// the answer the report is opened to ask and the layers cannot give it:
// "ready now" is a fact about blockers, "next" is a fact about the whole
// promotion rule (DESIGN §8).
func TestPrintedOrderNamesWhatEntersTheDesignQueue(t *testing.T) {
	next := &core.Ticket{Key: "ORC-7", Title: "Word the farewell", URL: "https://tracker.invalid/issue/ORC-7"}
	o := &core.Order{Next: core.Promotion{Ticket: next}}

	for _, render := range []func(*strings.Builder){
		func(b *strings.Builder) { printOrder(b, o, "") },
		func(b *strings.Builder) { printOrderMarkdown(b, o, "") },
	} {
		var b strings.Builder
		render(&b)
		got := b.String()
		if !strings.Contains(got, "ORC-7") || !strings.Contains(got, "design queue") {
			t.Errorf("the decision is missing:\n%s", got)
		}
		// Above the layers, not buried after them.
		if i := strings.Index(got, "ORC-7"); i > strings.Index(got, "Everything else") && strings.Contains(got, "Everything else") {
			t.Errorf("the decision is printed below the layers:\n%s", got)
		}
	}
}

// And the reason, when there is nothing to promote — a report that goes
// quiet in that case is the one that misleads, because the layers still
// list unblocked tickets the pipeline is not going to touch.
func TestPrintedOrderExplainsPromotingNothing(t *testing.T) {
	o := &core.Order{Next: core.Promotion{Why: "the queue is paused — B1 is open"}}
	for _, render := range []func(*strings.Builder){
		func(b *strings.Builder) { printOrder(b, o, "") },
		func(b *strings.Builder) { printOrderMarkdown(b, o, "") },
	} {
		var b strings.Builder
		render(&b)
		if got := b.String(); !strings.Contains(got, "B1 is open") {
			t.Errorf("the reason is missing:\n%s", got)
		}
	}
}

// The one case where the report's threshold and the sweep's differ, so
// the line that carries it has to survive rendering.
func TestPrintedOrderCarriesTheMergedBlockerNote(t *testing.T) {
	o := &core.Order{Layers: []core.Layer{{
		Name: core.LayerAfterFlight, Why: "every blocker is in flight.",
		Tickets: []core.OrderedTicket{{
			Key: "ORC-7", Title: "Word the farewell", URL: "https://tracker.invalid/issue/ORC-7",
			State:          protocol.Todo,
			DesignCanStart: true,
			BlockedBy:      []core.Neighbour{{Key: "ORC-3", Title: "Land it", State: protocol.Merged}},
		}},
	}}}
	for _, render := range []func(*strings.Builder){
		func(b *strings.Builder) { printOrder(b, o, "") },
		func(b *strings.Builder) { printOrderMarkdown(b, o, "") },
	} {
		var b strings.Builder
		render(&b)
		if got := b.String(); !strings.Contains(got, "design pass can start") {
			t.Errorf("the merged-blocker note is missing:\n%s", got)
		}
	}
}
