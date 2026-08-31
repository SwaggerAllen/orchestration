package core

import (
	"strconv"

	"github.com/SwaggerAllen/orchestration/internal/marker"
)

// markersOf parses every marker comment of the given kind on a ticket.
// Malformed markers are skipped here — the sweep counts what it can read,
// and a comment that fails to parse is surfaced by adapter-level logging
// rather than by wedging the whole sweep.
func markersOf(t *Ticket, kind marker.Kind) []marker.Marker {
	var out []marker.Marker
	for _, c := range t.Comments {
		m, ok, err := marker.Parse(c.Body)
		if err != nil || !ok {
			continue
		}
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

// hasMarkerField reports whether any marker of the kind carries field=value.
func hasMarkerField(t *Ticket, kind marker.Kind, field, value string) bool {
	for _, m := range markersOf(t, kind) {
		if m.Fields[field] == value {
			return true
		}
	}
	return false
}

// alreadyReportedCIRed reports whether this exact verdict is already on
// the ticket: the same run *and* the same attempt of it.
//
// Keyed on both because a re-run keeps the run's id and its URL and
// increments only the attempt. Keyed on the URL alone — which is how
// this started — a re-run that failed again for a real reason read as
// "already recorded", the sweep said nothing, and the ticket sat in
// Checks with red CI and no comment. The pipeline re-runs deliberately
// now (DESIGN §12), so that case is reachable rather than theoretical.
//
// A marker written before `run_attempt` existed carries no such field
// and is read as attempt 1, which is what it was: the first attempt is
// the only one anything could have reported back then.
func alreadyReportedCIRed(t *Ticket, runURL string, runAttempt int) bool {
	for _, m := range markersOf(t, marker.CIRed) {
		if m.Fields["run"] != runURL {
			continue
		}
		recorded := 1
		if v, err := strconv.Atoi(m.Fields["run_attempt"]); err == nil {
			recorded = v
		}
		if normalizeRunAttempt(recorded) == normalizeRunAttempt(runAttempt) {
			return true
		}
	}
	return false
}

// alreadyEscalatedBounce reports whether the second-bounce escalation has
// already fired at this many reconcile bounces.
//
// The escalation used to be a fact about state alone — `Ready for rework`
// plus two bounce markers — which made it a trap rather than a rule. Only
// the author moves a ticket out of `Blocked` and they choose the state
// (DESIGN §12), and `Ready for rework` is one of the states they may
// legitimately choose: sometimes the fix really is one more pass. The
// markers do not go away when they choose it, so the next sweep read the
// same two bounces and blocked it again. Catapult's ORC-174 went
// `Blocked` -> `Ready for rework` -> `Blocked` in twenty-seven seconds,
// posting two identical `blocked` comments with no reconciliation between
// them, and no gesture available to the author that the rule would not
// undo on the next tick.
//
// So the escalation records the count it fired at and fires again only
// when reconciliation bounces the ticket past it. A third bounce is new
// information and escalates; the author deciding to try once more is not,
// and stands.
//
// A marker written before this field existed carries no count, so it
// suppresses nothing — deliberately, since the alternative reads every
// older block as an escalation at the current count and would swallow a
// genuine third bounce. A ticket already parked at two escalates once
// more, and then holds.
func alreadyEscalatedBounce(t *Ticket, bounces int) bool {
	for _, m := range markersOf(t, marker.Blocked) {
		if v, err := strconv.Atoi(m.Fields["bounces"]); err == nil && v >= bounces {
			return true
		}
	}
	return false
}

// normalizeRunAttempt reads an absent attempt as the first one.
//
// GitHub numbers attempts from 1, so a zero here never came from the
// host: it is a verdict recorded before this field existed, or one from
// a reader that does not fill it. Either way the first attempt is what
// it was, and folding the two together is what keeps an old marker
// matching the run it was written for instead of re-firing on it.
func normalizeRunAttempt(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// hasDecisionlessPass reports whether the design agent declared a
// decisionless pass — no screens, no artifacts, no systems/*.md diff —
// which licenses Designing -> Ready for dev directly (DESIGN §3, §6, §9).
func hasDecisionlessPass(t *Ticket) bool {
	return len(markersOf(t, marker.DecisionlessPass)) > 0
}
