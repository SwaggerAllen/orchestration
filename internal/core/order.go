package core

import (
	"sort"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// Order answers "what should be designed next, and what can run beside
// it" from the graph the tracker already holds — blocking relations,
// mutex labels, states. It computes; it stores nothing.
//
// That is the whole point. The inputs change constantly and from several
// directions: the author adds a blocker, a design pass writes the mutex
// labels that decide what can run beside what, a ticket is canceled. Any
// written-down ordering is a cached copy of a pure function over all of
// that, and a stale ordering is worse than none because it is acted on.
// Note especially that the labels do not exist until the design pass
// that produces them has run — so an order computed when tickets are
// pulled into Todo cannot know what parallelises, and would be wrong by
// construction rather than by neglect.
//
// The layers answer a question about *waiting*, not about priority:
// which tickets are held up, by what, and what would free them.
type Order struct {
	Layers []Layer
}

// Layer is one band of the answer, in the order they are printed.
type Layer struct {
	Name string
	// Why states what membership in this layer means, so the output
	// explains itself to a reader who has not read this file.
	Why     string
	Tickets []OrderedTicket
}

// OrderedTicket is one ticket as the ordering reports it.
type OrderedTicket struct {
	Key   string
	Title string
	URL   string
	State protocol.State
	// BlockedBy and Blocks are the immediate neighbours, unresolved only:
	// a blocker that is already Done is not a fact about this ticket's
	// future, and listing it would make a ready ticket look held up.
	BlockedBy []Neighbour
	Blocks    []Neighbour
	// Milestone is the ticket's own, printed when the scope spans more
	// than one so a reader can see why something is held back by
	// scheduling rather than by the graph.
	Milestone string
	// Note explains a placement the blocker graph alone does not, which
	// today means exactly one thing: the ticket is outside the current
	// milestone.
	Note string
	// MutexHeldBy names an in-flight ticket sharing a mutex label with
	// this one. Not a blocker and not printed as one — design may run on
	// both at once — but promotion into Ready for dev would be reverted
	// (DESIGN §6), so a ticket that reads as ready is really queued, and
	// the design it produces is the kind most likely to be re-evaluated
	// before it lands (DESIGN §7).
	MutexHeldBy []Neighbour
}

// Neighbour is a related ticket, named enough to act on without a lookup.
type Neighbour struct {
	Key   string
	Title string
	State protocol.State
	// Label is the shared mutex label, on MutexHeldBy entries only.
	Label string
}

// Layer names, exported so a printer and its tests agree on them.
const (
	LayerStarted     = "In flight"
	LayerReady       = "Ready now"
	LayerAfterFlight = "Freed by what is in flight"
	LayerAfterReady  = "Freed by the two layers above, together"
	LayerRest        = "Everything else"
)

// ComputeOrder builds the layering.
//
// Scope: a milestone name limits the answer to it; "" spans the project.
// The wide case needs care, because milestones are worked in sequence
// and that constraint lives nowhere in the blocker graph — a ticket in a
// later milestone is commonly filed with no dependencies at all, since
// the milestone itself is the dependency. Read literally, such a ticket
// has nothing blocking it and lands in "Ready now" beside work that
// genuinely can start today, which is the report confidently
// recommending something that must not be started. So across a wide
// scope, anything outside the current milestone is held out of the
// startable layers and says why.
//
// Tickets already resolved are absent entirely — they are not waiting on
// anything and nothing waits on them.
func ComputeOrder(s *Snapshot, milestone string) *Order {
	byID := map[string]*Ticket{}
	var considered []*Ticket
	for _, t := range s.Tickets {
		byID[t.ID] = t
		if t.Resolved() || t.IsBoundary() {
			continue
		}
		if milestone != "" && t.Milestone != milestone {
			continue
		}
		considered = append(considered, t)
	}

	// openBlockers is the edge set that matters. A resolved blocker is
	// history; only what is still outstanding shapes the order.
	openBlockers := func(t *Ticket) []*Ticket {
		var out []*Ticket
		for _, id := range t.BlockedBy {
			if b := byID[id]; b != nil && !b.Resolved() {
				out = append(out, b)
			}
		}
		return out
	}

	started := map[string]bool{}
	for _, t := range considered {
		if isStarted(t) {
			started[t.ID] = true
		}
	}

	// Layer 2: nothing outstanding holds these back.
	ready := map[string]bool{}
	for _, t := range considered {
		if !started[t.ID] && len(openBlockers(t)) == 0 {
			ready[t.ID] = true
		}
	}
	// Layer 3: everything holding these back is already moving.
	afterFlight := map[string]bool{}
	for _, t := range considered {
		if started[t.ID] || ready[t.ID] {
			continue
		}
		if allIn(openBlockers(t), started) {
			afterFlight[t.ID] = true
		}
	}
	// Layer 4: freed once the first two layers are done, together.
	afterReady := map[string]bool{}
	firstTwo := union(started, ready)
	for _, t := range considered {
		if started[t.ID] || ready[t.ID] || afterFlight[t.ID] {
			continue
		}
		if allIn(openBlockers(t), firstTwo) {
			afterReady[t.ID] = true
		}
	}

	// Only when the scope spans milestones: asking for one by name is
	// asking about that one, and demoting all of it would answer a
	// question nobody put.
	gated := map[string]bool{}
	if milestone == "" && s.CurrentMilestone != "" {
		for _, t := range considered {
			if t.Milestone != s.CurrentMilestone && !started[t.ID] {
				gated[t.ID] = true
			}
		}
	}

	o := &Order{Layers: []Layer{
		{Name: LayerStarted, Why: "already past Todo — the pipeline is working these now."},
		{Name: LayerReady, Why: "nothing outstanding blocks them; these are the candidates to start."},
		{Name: LayerAfterFlight, Why: "every blocker is in flight, so these free themselves as those land."},
		{Name: LayerAfterReady, Why: "blocked only by the two layers above; they need something started first."},
		{Name: LayerRest, Why: "blocked by something itself blocked, or held back by a milestone rather than by the graph."},
	}}
	for _, t := range considered {
		i := 4
		switch {
		case started[t.ID]:
			i = 0
		case gated[t.ID]:
			i = 4
		case ready[t.ID]:
			i = 1
		case afterFlight[t.ID]:
			i = 2
		case afterReady[t.ID]:
			i = 3
		}
		d := describe(t, byID, openBlockers, considered)
		if milestone == "" {
			d.Milestone = t.Milestone
		}
		if gated[t.ID] {
			d.Note = "outside the current milestone (" + s.CurrentMilestone + ") — milestones are worked in sequence, so nothing here starts until that one drains, whatever its blockers say"
		}
		o.Layers[i].Tickets = append(o.Layers[i].Tickets, d)
	}
	for i := range o.Layers {
		ts := o.Layers[i].Tickets
		sort.Slice(ts, func(a, b int) bool { return orderBefore(byID, ts[a], ts[b]) })
	}
	return o
}

// isStarted reports a ticket the pipeline is already working. Blocked
// counts: it has left Todo and it is not a candidate to start, so the one
// place it belongs is beside the rest of the in-flight work — where its
// state is printed and the tickets waiting on it can be seen waiting on
// something that will not move by itself.
func isStarted(t *Ticket) bool {
	switch t.State {
	case protocol.Backlog, protocol.Todo:
		return false
	}
	return true
}

func describe(t *Ticket, byID map[string]*Ticket, openBlockers func(*Ticket) []*Ticket, considered []*Ticket) OrderedTicket {
	ot := OrderedTicket{Key: t.Key, Title: t.Title, URL: t.URL, State: t.State}
	for _, b := range openBlockers(t) {
		ot.BlockedBy = append(ot.BlockedBy, Neighbour{Key: b.Key, Title: b.Title, State: b.State})
	}
	for _, id := range t.Blocks {
		if b := byID[id]; b != nil && !b.Resolved() {
			ot.Blocks = append(ot.Blocks, Neighbour{Key: b.Key, Title: b.Title, State: b.State})
		}
	}
	// Only for tickets not yet started: an in-flight ticket already holds
	// its labels, and telling it that it collides with itself is noise.
	if !isStarted(t) {
		for _, other := range considered {
			if other.ID == t.ID || !other.InFlight() {
				continue
			}
			for _, mine := range t.MutexLabels() {
				if other.HasLabel(mine) {
					ot.MutexHeldBy = append(ot.MutexHeldBy, Neighbour{
						Key: other.Key, Title: other.Title, State: other.State, Label: mine,
					})
				}
			}
		}
	}
	return ot
}

// orderBefore sorts within a layer by the same precedence the dispatcher
// uses, so the list reads in the order the pipeline would actually take
// them rather than in an order invented for the report.
func orderBefore(byID map[string]*Ticket, a, b OrderedTicket) bool {
	ta, tb := byKey(byID, a.Key), byKey(byID, b.Key)
	if ta == nil || tb == nil {
		return a.Key < b.Key
	}
	return Precedes(ta, tb)
}

func byKey(byID map[string]*Ticket, key string) *Ticket {
	for _, t := range byID {
		if t.Key == key {
			return t
		}
	}
	return nil
}

func allIn(ts []*Ticket, set map[string]bool) bool {
	for _, t := range ts {
		if !set[t.ID] {
			return false
		}
	}
	return len(ts) > 0
}

func union(a, b map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}
