// Package state is the pipeline's record of its own writes.
//
// It exists because the tracker cannot answer "who moved this ticket".
// On a solo workspace the harness holds the author's API key, so every
// write the pipeline makes arrives wearing the author's identity — and a
// role resolved from that identity answered "control plane" for the
// human's moves too, which is the one role the revert rules trust. Every
// §9 invariant was off, silently, for as long as the two shared an id.
// Buying the pipeline its own tracker account would fix it for a seat
// per role per month, to encode something the pipeline already knows.
//
// So the pipeline writes down what it is about to do, and the sweep
// compares that record against where the tracker says the ticket is.
// Agreement means the pipeline made the last move; disagreement means
// somebody else did.
//
// **Write-ahead, always.** The record goes down before the transition,
// never after. A record describing a transition that then failed is
// harmless — the tracker still shows the old state, so nothing matches
// it and the next attempt writes the same intent. A transition that
// landed without its record is not harmless: it reads as a human's, gets
// reverted, re-made, and reverted again.
//
// **Fail closed on write, open on read.** If the record cannot be
// written the transition does not happen, and the sweep — being
// convergent — tries again next beat. If the store cannot be read,
// nothing is judged. Both directions fail toward doing nothing rather
// than toward a revert loop.
package state

import (
	"context"
	"sync"

	"github.com/SwaggerAllen/orchestration/internal/core"
)

// Store is the port. One implementation talks to the metronome Worker's
// Durable Object; the other is a map, for Ring 1 and Ring 2.
type Store interface {
	// Record writes down a move the pipeline is about to make. It must
	// return an error rather than succeed silently: the caller declines
	// to transition when this fails.
	Record(ctx context.Context, ticketID string, m core.RecordedMove) error
	// All returns every recorded move for the project, keyed by ticket
	// id. Tickets with no record are absent rather than zero-valued —
	// "I have no record" and "I left it in the zero state" are different
	// answers and only one of them means "do not judge".
	All(ctx context.Context) (map[string]core.RecordedMove, error)
}

// Memory is the in-process Store for tests and the sim.
type Memory struct {
	mu sync.Mutex
	m  map[string]core.RecordedMove
	// FailWrites makes Record error, standing in for a store that is
	// unreachable — the case whose whole point is that the transition
	// must not happen anyway.
	FailWrites error
	// FailReads makes All error, standing in for the same outage on the
	// read side, where the safe answer is to judge nothing.
	FailReads error
}

func NewMemory() *Memory { return &Memory{m: map[string]core.RecordedMove{}} }

func (s *Memory) Record(_ context.Context, ticketID string, m core.RecordedMove) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailWrites != nil {
		return s.FailWrites
	}
	if s.m == nil {
		s.m = map[string]core.RecordedMove{}
	}
	s.m[ticketID] = m
	return nil
}

func (s *Memory) All(_ context.Context) (map[string]core.RecordedMove, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailReads != nil {
		return nil, s.FailReads
	}
	out := make(map[string]core.RecordedMove, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out, nil
}
