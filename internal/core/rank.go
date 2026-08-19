package core

import (
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// precedenceRank orders tickets for the precedence rule (DESIGN §7):
// furthest along the pipeline first. Rework states outrank their first-pass
// counterparts and Checks because a bounced ticket reached Reconciling
// once — work already invested is worth more than work not yet started.
// Blocked, Done and Canceled never compete for dispatch, so their rank is
// irrelevant; they get 0.
var precedenceRank = map[protocol.State]int{
	protocol.Backlog:        0,
	protocol.Todo:           10,
	protocol.ReadyForDesign: 15,
	protocol.Designing:      20,
	protocol.DesignReview:   30,
	protocol.ReadyForDev:    40,
	protocol.InProgress:     50,
	protocol.Checks:         60,
	protocol.ReadyForRework: 70,
	protocol.Reworking:      80,
	protocol.Reconciling:    90,
	protocol.Merged:         100,
	protocol.BoundaryReview: 0,
	protocol.Blocked:        0,
	protocol.Done:           0,
	protocol.Canceled:       0,
}

// Precedes reports whether a should be picked before b: urgent first, then
// furthest along, oldest breaks ties (DESIGN §7). Key breaks the final tie
// so ordering is total and sweeps are deterministic.
func Precedes(a, b *Ticket) bool {
	if a.Urgent() != b.Urgent() {
		return a.Urgent()
	}
	ra, rb := precedenceRank[a.State], precedenceRank[b.State]
	if ra != rb {
		return ra > rb
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.Key < b.Key
}

// forwardRank orders states along the happy path toward Done, for the §9
// invariant "no forward transition while re-evaluate is set". Rework states
// share their dev counterparts' rank: a bounce backward is never "forward",
// and rework resuming is. Blocked and Canceled are absent — a transition
// into or out of them is never forward, because Blocked hand-offs are the
// author's judgment (DESIGN §12) and cancellation is a verdict, not
// progress.
var forwardRank = map[protocol.State]int{
	protocol.Backlog:        0,
	protocol.Todo:           1,
	protocol.ReadyForDesign: 2,
	protocol.Designing:      3,
	protocol.DesignReview:   4,
	protocol.ReadyForDev:    5,
	protocol.ReadyForRework: 5,
	protocol.InProgress:     6,
	protocol.Reworking:      6,
	protocol.Checks:         7,
	protocol.Reconciling:    8,
	protocol.Merged:         9,
	protocol.Done:           10,
}

// forward reports whether a transition moves toward Done.
func forward(from, to protocol.State) bool {
	f, okF := forwardRank[from]
	t, okT := forwardRank[to]
	return okF && okT && t > f
}
