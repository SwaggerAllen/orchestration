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
			MutexHeldBy: []core.Neighbour{{
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
	// The mutex line must not read as a blocker, or the reader will treat
	// a designable ticket as unavailable.
	if !strings.Contains(got, "designing this now is legal") {
		t.Errorf("the mutex note reads as a blocker:\n%s", got)
	}
	// And it says the ordering is derived, so nobody pastes it somewhere
	// as a plan and acts on it a week later.
	if !strings.Contains(got, "nothing here is stored") {
		t.Errorf("the report does not say it is derived:\n%s", got)
	}
}
