package core

import (
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

// hasDecisionlessPass reports whether the design agent declared a
// decisionless pass — no screens, no artifacts, no systems/*.md diff —
// which licenses Designing -> Ready for dev directly (DESIGN §3, §6, §9).
func hasDecisionlessPass(t *Ticket) bool {
	return len(markersOf(t, marker.DecisionlessPass)) > 0
}
