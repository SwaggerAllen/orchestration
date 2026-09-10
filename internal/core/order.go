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
	// Next is what the control plane will move into the design queue, or
	// the reason it will move nothing (DESIGN §8).
	//
	// On the report rather than left to the reader, because the layers
	// cannot answer it. *Ready now* is a fact about blockers; *next* is a
	// fact about the pause, the queue's depth, the milestone and the
	// blocker graph together, and those are not the same set. A reader
	// looking at three unblocked tickets while the pipeline promotes none
	// of them has been told the truth and misled by it.
	Next Promotion
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
	// Uncommitted marks a ticket carrying no milestone at all.
	//
	// A separate field rather than an empty Milestone, because that
	// string is also empty for every ticket in a single-milestone scope,
	// where printing "no milestone" on every line would be false. The
	// report knows which case it is in; the renderer should not have to
	// infer it.
	Uncommitted bool
	// Note explains a placement the blocker graph alone does not: the
	// ticket is outside the current milestone, or it carries none at
	// all. The two are exclusive — being outside one requires having
	// one.
	Note string
	// DesignCanStart marks a ticket whose blockers are all outstanding
	// and all at `Merged`.
	//
	// It stays where the layering puts it — *freed by what is in flight*,
	// not *ready now* — because moving it would be wrong about the half
	// of the pipeline the layering is mostly about: dev pickup keeps
	// `Resolved`, since a blocker that fails its post-deploy check goes
	// to `Blocked` and code built on it has to be re-examined. Design's
	// threshold is lower, because a design pass reads `main` and merged
	// work is on `main` (DESIGN §8).
	//
	// So this flag is the one place the report and the control plane
	// would otherwise disagree, and it exists to carry that difference
	// rather than to hide it.
	DesignCanStart bool
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
// Scope: a milestone name limits the answer to it; "" spans the project,
// which is the default the report is asked for. The wide case needs
// care, because milestones are worked in sequence and that constraint
// lives nowhere in the blocker graph — a ticket in a later milestone is
// commonly filed with no dependencies at all, since the milestone itself
// is the dependency. Read literally, such a ticket has nothing blocking
// it and lands in "Ready now" beside work that genuinely can start
// today, which is the report confidently recommending something that
// must not be started. So across a wide scope, anything outside the
// current milestone is held out of the startable layers and says why.
//
// A ticket carrying no milestone is startable but uncommitted, and the
// two are separate axes. It is not held out of the startable layers,
// because nothing sequences it and the dispatchers do not read
// milestones at all — the queue will take it the moment it reaches
// Designing. But it is absent from any named milestone's scope, because
// a milestone's roster is the work committed to it and nobody has
// committed this. So the wide view names it and the narrow view does
// not, which is the difference between "ready now" and "in this
// milestone".
//
// The wide view takes a bare ticket from Todo onward, and that
// narrowing is the point of showing them at all. Todo is somebody
// saying the work is ready with only the scheduling lagging, and
// anything past it is already moving — both are facts a "what next"
// report has to carry. A bare ticket still in Backlog is an idea, and
// one in a triage-category state is a proposal nobody has accepted;
// listing those beside startable work is how this report turns into the
// whole tracker.
//
// Every bare ticket it does take says so, in a note. The state that
// admits it — Todo — is also the state a ticket lands in when the
// author accepts a proposal and the milestone assignment lags, so
// "startable and uncommitted" and "committed and I forgot to say so"
// look identical here. The report cannot tell them apart and does not
// try; it names the fact and lets the author recognise their own
// oversight.
//
// It arrives that way honestly: the boundary files proposals into
// Triage, accepting one means moving it out and assigning a milestone,
// and assigning a milestone is a commitment the author owns (DESIGN
// §10). So the accept happens and the assignment lags. Measured on
// Catapult: four tech-debt tickets in Todo — ORC-48, ORC-50, ORC-51,
// ORC-52 — invisible to a report that named one.
//
// Tickets already resolved are absent entirely: they are not waiting on
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
		if t.Milestone == "" && t.State != protocol.Todo && !isStarted(t) {
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
			// A ticket carrying no milestone is not held back by one.
			// Startable and committed are different axes: the gate asks
			// "is a milestone in front of this", and the answer for a
			// bare ticket is no — nothing sequences it, and neither
			// dispatcher filters on milestone, so the queue will take it
			// as soon as it reaches Designing. It is uncommitted, not
			// future, and those want opposite treatment.
			if t.Milestone == "" || started[t.ID] {
				continue
			}
			if t.Milestone != s.CurrentMilestone {
				gated[t.ID] = true
			}
		}
	}

	o := &Order{Layers: []Layer{
		{Name: LayerStarted, Why: "already past Todo — the pipeline is working these now."},
		{Name: LayerReady, Why: "nothing outstanding blocks them; the control plane promotes the first of these into the design queue."},
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
		d.Uncommitted = t.Milestone == ""
		d.DesignCanStart = !started[t.ID] && allOnMain(openBlockers(t))
		switch {
		case gated[t.ID]:
			d.Note = "outside the current milestone (" + s.CurrentMilestone + ") — milestones are worked in sequence, so nothing here starts until that one drains, whatever its blockers say"
		case t.Milestone == "":
			// Mutually exclusive with the gate above, which only fires
			// on a ticket that has a milestone to be outside of.
			d.Note = "no milestone, so the control plane will not promote it — nothing sequences it, but committing it is the author's move. If the milestone is missing by oversight rather than by choice, this is where that shows"
		}
		o.Layers[i].Tickets = append(o.Layers[i].Tickets, d)
	}
	for i := range o.Layers {
		ts := o.Layers[i].Tickets
		sort.Slice(ts, func(a, b int) bool { return orderBefore(byID, ts[a], ts[b]) })
	}
	// Deliberately not scoped by `milestone`. Promotion is always about
	// the current one, so narrowing the report does not narrow the fact —
	// asking what is in a milestone does not stop the pipeline from being
	// about to move something.
	o.Next = NextPromotion(s)
	return o
}

// allOnMain reports blockers that are all outstanding and all merged: the
// case where a design pass can start although the layering has the ticket
// waiting. Empty is false — a ticket with nothing outstanding is simply
// ready, and saying "design can start" on it would be noise on every
// ticket in *ready now*.
func allOnMain(open []*Ticket) bool {
	if len(open) == 0 {
		return false
	}
	for _, b := range open {
		if !onMain(b) {
			return false
		}
	}
	return true
}

// isStarted reports a ticket the pipeline is already working. Blocked
// counts: it has left Todo and it is not a candidate to start, so the one
// place it belongs is beside the rest of the in-flight work — where its
// state is printed and the tickets waiting on it can be seen waiting on
// something that will not move by itself.
func isStarted(t *Ticket) bool {
	switch t.State {
	// Ready for design belongs with the other two: it is a queue, and a
	// queue is work waiting rather than work happening. It used to be
	// absent from this list because it did not exist — Designing meant
	// both, and reading a queued ticket as in flight is exactly the
	// confusion splitting them removed.
	case protocol.Backlog, protocol.Todo, protocol.ReadyForDesign, protocol.ReadyForRedesign:
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
			if other.ID == t.ID || !other.HoldsMutex() {
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
