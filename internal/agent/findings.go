package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/plane"
)

// HarnessFinding is one problem an agent hit in the pipeline itself.
//
// Agents already notice these constantly and had nowhere to put them.
// Three separate passes reported that no `Base:` sha was recorded, in
// prose, in three hand-backs nobody aggregates; reconcile posted four
// callouts on one ticket explicitly saying "worth a ticket"; none of it
// reached the boundary scan, whose inputs (DESIGN §10) are diffs, TODOs,
// skipped tests and dependency drift. The one channel agents have for
// reporting that the harness is broken reached nobody whose job is
// filing it.
//
// Deliberately narrow. This is not a general "file what you noticed"
// channel: an agent that reads a whole product in an afternoon is very
// good at spotting gaps and very bad at judging whether a gap is news
// (DESIGN §4), and a queue full of confident product opinions is worse
// than no queue. Harness findings are the exception because the author
// is the only person who can fix the pipeline and the agent is the only
// one who sees it fail. Product debt keeps its existing route: the
// boundary's own bounded scan.
type HarnessFinding struct {
	// Title is the imperative one-liner a ticket would carry.
	Title string `json:"title"`
	// Detail is what happened and why it matters — the part that stops
	// this being a bug report with no reproduction.
	Detail string `json:"detail"`
	// Dedupe keys the finding so ten runs hitting the same gap produce
	// one ticket. Stable across runs by construction: name the thing,
	// not the run.
	Dedupe string `json:"dedupe"`
	// Kind is what the finding is about: KindHarness for the pipeline,
	// KindProject for the repository the pass is working in. Empty means
	// harness, which is what every finding recorded before this field
	// existed was.
	//
	// The distinction was stated sharply in the prompt and enforced
	// nowhere, and it lost against an incentive. A hand-back is prose in
	// a ticket comment: not collected, not deduped, not carried past the
	// archive, not surfaced to any boundary. A harness finding is all
	// four. So an agent that found a real project defect outside its
	// scope, and wanted it to survive, had exactly one structured
	// channel and it was labelled `harness`.
	//
	// Measured on Catapult's ORC-90: of twelve findings carried past the
	// archive, two were plainly about the project — a ticket's own text
	// against the loader it described, and a worked example in a doc
	// that the loader rejects. Neither would have been true on a project
	// using none of this machinery, which is the prompt's own test.
	//
	// A field rather than a second findings file, for the reason the
	// non-asks scope is a field: this is a property of the finding, and
	// two files is two places to explain and two write paths to get
	// wrong.
	Kind string `json:"kind,omitempty"`
}

// The two kinds a finding can carry.
const (
	// KindHarness is a problem with the pipeline: a check that passed
	// without checking, a value the protocol promises and does not
	// deliver, a credential the run needed and lacked.
	KindHarness = "harness"
	// KindProject is a defect in the repository the pass is working in,
	// found outside the pass's own scope. It reaches the boundary's debt
	// scan rather than its harness list.
	KindProject = "project"
)

// IsProject reports whether this finding is about the project rather
// than the pipeline. Empty reads as harness: every finding recorded
// before the field existed was one, and reading them as project findings
// would move a milestone's worth of pipeline problems into the debt
// backlog on the first run after an upgrade.
func (f HarnessFinding) IsProject() bool { return f.Kind == KindProject }

// LoadHarnessFindings reads and validates the findings file. An absent
// file is not an error — most runs have nothing to report, and requiring
// an empty array would make "no findings" a thing agents forget to say
// rather than a thing they say by saying nothing.
func LoadHarnessFindings(path string) ([]HarnessFinding, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("harness findings: %w", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var fs []HarnessFinding
	if err := json.Unmarshal(raw, &fs); err != nil {
		return nil, fmt.Errorf("harness findings %s: %w", path, err)
	}
	for i, f := range fs {
		if strings.TrimSpace(f.Title) == "" || strings.TrimSpace(f.Dedupe) == "" {
			return nil, fmt.Errorf("harness finding %d: title and dedupe are required — without the key the same gap files a ticket every run", i)
		}
		if strings.TrimSpace(f.Detail) == "" {
			return nil, fmt.Errorf("harness finding %d (%s): no detail — a finding nobody can act on costs a read and buys nothing", i, f.Title)
		}
		// Empty is legal and means harness; a wrong spelling is not.
		// Silently reading "porject" as harness would put a project
		// defect back in the list this field exists to keep clean, and
		// the run is the only place that still knows which was meant.
		switch f.Kind {
		case "", KindHarness, KindProject:
		default:
			return nil, fmt.Errorf("harness finding %d (%s): kind %q — %q for the pipeline, %q for this repository, or omit it for %q",
				i, f.Title, f.Kind, KindHarness, KindProject, KindHarness)
		}
	}
	return fs, nil
}

// PostHarnessFindings records each finding as a marker comment on the
// ticket the run was working. The ticket is where the run's audit trail
// already lives, and the boundary reads them back from there — the agent
// itself writes nothing to the tracker (DESIGN §9).
func PostHarnessFindings(ctx context.Context, p *plane.Plane, ticketID string, fs []HarnessFinding) error {
	for _, f := range fs {
		// The marker keeps its kind — it is recorded wire format in every
		// project's ticket history, and renaming it would orphan every
		// finding already written. What the finding is *about* travels
		// as a field, absent meaning harness.
		fields := map[string]string{
			"id":    f.Dedupe,
			"title": f.Title,
		}
		heading := "Harness finding"
		routed := "Recorded for the milestone boundary, which decides whether it becomes a ticket (DESIGN §10). Not a change to this ticket's scope."
		if f.IsProject() {
			fields["kind"] = KindProject
			heading = "Project finding"
			routed = "Recorded for the milestone boundary's debt scan, which decides whether it becomes a ticket (DESIGN §10). Not a change to this ticket's scope — a defect that blocks this milestone is filed against it directly instead."
		}
		m := marker.Marker{Kind: marker.HarnessFinding, Fields: fields}
		prose := fmt.Sprintf("**%s: %s**\n\n%s\n\n%s", heading, f.Title, f.Detail, routed)
		if err := p.CommentTicket(ctx, ticketID, m.Comment(prose)); err != nil {
			return err
		}
	}
	return nil
}

// CollectHarnessFindings gathers the findings recorded across a
// project's open tickets, newest last, deduped by key.
//
// Read from live tickets only, which bounds it the way the rest of the
// boundary's inputs are bounded: the archive pass has already removed
// the last milestone's Done work, so what remains is this milestone's.
func CollectHarnessFindings(tickets []*core.Ticket) []HarnessFinding {
	seen := map[string]bool{}
	var out []HarnessFinding
	for _, t := range tickets {
		for _, c := range t.Comments {
			m, ok, err := marker.Parse(c.Body)
			if err != nil || !ok || m.Kind != marker.HarnessFinding {
				continue
			}
			id := m.Fields["id"]
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			// The prose carries the detail; the marker carries what a
			// tool needs. Both travel, because the boundary agent reads
			// the detail and the dedupe check reads the key.
			_, detail, _ := strings.Cut(c.Body, "\n")
			out = append(out, HarnessFinding{
				Title:  m.Fields["title"],
				Detail: strings.TrimSpace(detail),
				Dedupe: id,
				// Absent reads as harness, which is what every marker
				// written before this field existed was.
				Kind: m.Fields["kind"],
			})
		}
	}
	return out
}
