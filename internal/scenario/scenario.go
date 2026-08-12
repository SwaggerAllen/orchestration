// Package scenario is the Ring-3 rehearsal harness (PLAN §2): reset a
// disposable project to nothing, seed it from a fixture, let the real
// pipeline run against it, then check what came out.
//
// It lives in the pipeline repo and is deliberately absent from
// examples/stubs. Reset destroys tickets, and a destructive command
// shipped as a stub is a destructive command that eventually lands in a
// real project repo. Nothing here is ever copied to a project.
//
// The fixture is the point. "Run it again and see if it still works"
// only means something if the input is identical every time; what the
// agents do with that input is where the variability belongs, and the
// check phase is written to tolerate it — it asserts on where tickets
// ended up and what the pipeline recorded, never on prose a model wrote.
package scenario

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// Scenario is one rehearsal: what to create, and what should be true
// when the pipeline has finished with it.
type Scenario struct {
	// Name identifies the scenario in output and in run titles.
	Name string `json:"name"`
	// Description says what this rehearsal is meant to prove. Read by
	// humans reviewing a failed run, so it earns its place.
	Description string `json:"description"`

	// Milestones are created in listed order if absent, and reused if
	// present. Reuse rather than recreation keeps the sort order stable
	// across runs, which matters because "the next milestone" is defined
	// by that order (DESIGN §10).
	Milestones []Milestone `json:"milestones"`

	// Tickets are created in listed order. Order is also the tiebreak
	// the pipeline's oldest-first rule will see, so it is part of the
	// fixture, not an accident of iteration.
	Tickets []Ticket `json:"tickets"`

	// Expect is asserted by the check phase.
	Expect Expect `json:"expect"`
}

type Milestone struct {
	Name string `json:"name"`
	// SortOrder is explicit so a scenario cannot depend on whatever
	// order the tracker happens to return.
	SortOrder float64 `json:"sortOrder"`
}

// Ticket is a seeded issue. Deliberately narrow: a scenario describes
// work to be done, not pipeline state to be faked. Seeding a ticket
// directly into a mid-pipeline state would rehearse a situation the
// pipeline never produces.
type Ticket struct {
	// Ref names this ticket within the scenario so Expect can refer to
	// it without knowing the key the tracker will assign.
	Ref         string   `json:"ref"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
	Milestone   string   `json:"milestone"`
	Priority    int      `json:"priority"`
	// State is where the ticket starts. Restricted to the states an
	// author actually puts work in.
	State protocol.State `json:"state"`
}

// Expect is the pass condition, written in terms the pipeline controls
// rather than terms a model does.
type Expect struct {
	// FinalStates maps a ticket ref to the state it must reach.
	FinalStates map[string]protocol.State `json:"finalStates"`
	// Files are repository paths that must exist when the run is done —
	// the design pass's artifacts, chiefly. Content is not asserted:
	// that is the model's to vary.
	Files []string `json:"files"`
	// Markers are pipeline marker kinds that must appear on a ticket,
	// keyed by ref. This is how a scenario asserts that a hop actually
	// happened rather than that a state was reached by some other route.
	Markers map[string][]string `json:"markers"`
	// AbsentMarkers are marker kinds that must NOT appear, keyed by ref.
	// A happy path that quietly escalated is not a happy path.
	AbsentMarkers map[string][]string `json:"absentMarkers"`
}

// Load reads and validates a scenario file. Validation is strict
// because a typo in a fixture is a rehearsal that silently proves less
// than it claims — a ref that no ticket defines would assert nothing.
func Load(path string) (*Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Scenario
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("scenario %s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("scenario %s: %w", path, err)
	}
	return &s, nil
}

// seedableStates are where an author puts real work. Anything further
// along is pipeline-owned: seeding into it would rehearse a state the
// pipeline never creates, and prove nothing about how it got there.
var seedableStates = map[protocol.State]bool{
	protocol.Backlog:   true,
	protocol.Todo:      true,
	protocol.Designing: true,
}

func (s *Scenario) Validate() error {
	var problems []string
	add := func(f string, args ...any) { problems = append(problems, fmt.Sprintf(f, args...)) }

	if s.Name == "" {
		add("name: missing")
	}
	if len(s.Tickets) == 0 {
		add("tickets: a scenario with no tickets rehearses nothing")
	}

	milestones := map[string]bool{}
	for _, m := range s.Milestones {
		if m.Name == "" {
			add("milestones: a milestone with no name")
			continue
		}
		if milestones[m.Name] {
			add("milestones: %q listed twice", m.Name)
		}
		milestones[m.Name] = true
	}

	refs := map[string]bool{}
	for i, t := range s.Tickets {
		where := fmt.Sprintf("tickets[%d]", i)
		if t.Ref == "" {
			add("%s: ref is required — Expect refers to tickets by ref", where)
		} else if refs[t.Ref] {
			add("%s: ref %q is used twice", where, t.Ref)
		}
		refs[t.Ref] = true
		if t.Title == "" {
			add("%s (%s): title is required", where, t.Ref)
		}
		if !seedableStates[t.State] {
			add("%s (%s): state %q is not a state an author seeds work in", where, t.Ref, t.State)
		}
		if t.Milestone != "" && !milestones[t.Milestone] {
			add("%s (%s): milestone %q is not declared in this scenario", where, t.Ref, t.Milestone)
		}
	}

	for ref := range s.Expect.FinalStates {
		if !refs[ref] {
			add("expect.finalStates: %q is not a ticket in this scenario", ref)
		}
	}
	for ref := range s.Expect.Markers {
		if !refs[ref] {
			add("expect.markers: %q is not a ticket in this scenario", ref)
		}
	}
	for ref := range s.Expect.AbsentMarkers {
		if !refs[ref] {
			add("expect.absentMarkers: %q is not a ticket in this scenario", ref)
		}
	}
	if len(s.Expect.FinalStates) == 0 && len(s.Expect.Files) == 0 {
		add("expect: asserts nothing, so the run can only fail by crashing")
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("invalid:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}
