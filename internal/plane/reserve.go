package plane

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/core"
)

// DispatchReservationTTL bounds a dispatch reservation (DESIGN §6).
//
// **A bound, not a measurement.** What it has to outlive is the window
// between `workflow_dispatch` returning and the run becoming visible to
// the next sweep, and the only figure this repo has actually taken for
// that is the ninety seconds `core.VerifyPickup` records between a
// dispatch and the proof it happened. Five minutes is a wide margin over
// that figure, and it is well inside every `staleClaimGrace` a project
// has configured — which matters, because the cost of being too generous
// is an agent kind idle after a job that died before it ever claimed, and
// that must not outlast the point where the pipeline already parks the
// ticket.
//
// It matches the Durable Object's own fallback deliberately
// (`worker/projectstate.ts`). The store defaults to five minutes when a
// caller sends no usable TTL, and two halves of one mechanism disagreeing
// about how long a lock lasts is the kind of thing that is only ever
// discovered from the outside.
//
// Per-project tuning would mean a `pipeline.config.json` key, and that
// file is author-owned in every project (DESIGN §5) — so it would have to
// land there first, in its own change. Not taken now because no project
// has yet wanted a different number; the DO's comment ("the TTL is the
// caller's, because only the caller knows how long boot-to-claim takes
// for that project") is where to start if one does.
const DispatchReservationTTL = 5 * time.Minute

// reserveDispatch claims an agent kind for a ticket before dispatching
// it. It returns the ticket id already holding the kind, or "" when the
// caller may dispatch.
//
// A project with no state store gets "" and no reservation, which is the
// same fail-open the §9 invariants already take there: `pipeline
// preflight` prints "no state store configured — transitions are
// unrecorded and the DESIGN 9 invariants are not enforced", and this is
// one more thing in that sentence rather than a new silence.
func (p *Plane) reserveDispatch(ctx context.Context, kind core.AgentKind, ticketID string) (string, error) {
	if p.State == nil {
		return "", nil
	}
	return p.State.Reserve(ctx, kind, ticketID, DispatchReservationTTL)
}

// releaseDispatch drops a reservation. Best-effort by construction — the
// TTL is what keeps correctness from depending on it arriving — so a
// failure is reported and not returned.
//
// Reported rather than swallowed, though. The consequence of a lost
// release is an agent kind idle for up to the TTL, which looks from the
// outside exactly like a pipeline that has quietly stopped dispatching;
// a line in the run log is the difference between diagnosing that in a
// minute and diagnosing it by reading this file.
//
// Scoped to the ticket, which is what makes it safe to call from a run
// that just lost a race: the store deletes the reservation only when the
// holder matches, so a loser releasing cannot free the winner's lock.
func (p *Plane) releaseDispatch(ctx context.Context, kind core.AgentKind, ticketID string) {
	if p.State == nil {
		return
	}
	if err := p.State.Release(ctx, kind, ticketID); err != nil {
		fmt.Fprintf(os.Stderr, "pipeline: releasing the %s reservation on %s failed (%v) — "+
			"it expires on its own within %s, and until then no %s agent dispatches\n",
			kind, p.keyOf(ticketID), err, DispatchReservationTTL, kind)
	}
}

// ReleaseDispatchReservation is the agent-side half: a claim that got
// through core.VerifyPickup is past the window the reservation exists to
// cover, so holding it any longer only idles the kind.
//
// At claim rather than at the end of the run, and the difference is not
// bookkeeping. The reservation covers dispatch-to-visible; once a run has
// claimed, it is unambiguously visible in the run list and the ordinary
// singularity guard has it. Holding until the run ends would instead put
// the whole run's length inside the TTL, which forces the TTL up, which
// is the one direction that hurts: a long TTL on a job that died before
// claiming is an agent kind that does nothing for that long.
func (p *Plane) ReleaseDispatchReservation(ctx context.Context, kind core.AgentKind, ticketID string) {
	p.releaseDispatch(ctx, kind, ticketID)
}

// keyOf names a ticket the way a human reading the log does. Falls back
// to the id, because a message about a ticket the snapshot does not carry
// is still worth printing — this is diagnostics, and refusing to print
// the awkward case is how a log stops being trusted.
func (p *Plane) keyOf(ticketID string) string {
	if k, ok := p.keyByID[ticketID]; ok {
		return k
	}
	return ticketID
}
