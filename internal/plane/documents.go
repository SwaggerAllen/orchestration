package plane

import (
	"context"
	"fmt"
	"strings"
)

// NonAsks returns the body of the project's confirmed non-asks document
// (DESIGN §4) and whether the project has one.
//
// The agents cannot fetch this themselves and are not going to be able
// to: the harness owns every tracker read and write (DESIGN §9), so the
// only way a proposal gets checked against what the author already
// refused is for the harness to read the document and put it in the
// prompt. Before this existed the design agent reported the gap itself —
// "this run has no tracker access, so I could not check the sketch's
// decisions against it" — which is the right behaviour and the wrong
// outcome.
//
// Matching is case-insensitive on the title: the document is typed by a
// human into a tracker UI, and "Confirmed Non-Asks" is the same document
// as "Confirmed non-asks" to everyone but a string comparison.
func (p *Plane) NonAsks(ctx context.Context) (body string, found bool, err error) {
	want := p.Config.NonAsksDocument
	if want == "" {
		return "", false, nil
	}
	docs, err := p.Tracker.ListProjectDocuments(ctx, p.Config.Tracker.ProjectID)
	if err != nil {
		return "", false, err
	}
	var hits []string
	for _, d := range docs {
		if !strings.EqualFold(strings.TrimSpace(d.Title), strings.TrimSpace(want)) {
			continue
		}
		hits = append(hits, d.Title)
		body = d.Content
	}
	// Two documents by the same name is not a thing to resolve by
	// picking one: the agent would be told the author's constraints and
	// silently shown half of them.
	if len(hits) > 1 {
		return "", false, fmt.Errorf("plane: project has %d documents titled %q (%s) — the pipeline reads one, so rename or merge them",
			len(hits), want, strings.Join(hits, ", "))
	}
	return body, len(hits) == 1, nil
}
