// Package marker defines the grammar of programmatic pipeline comments
// (DESIGN §9). Markers are the API between components — escalation counts
// them, boundary resume reads them, claims record them — so the grammar is
// specified and round-trip tested here rather than left as a convention that
// the sim and production could quietly disagree on.
//
// A marker is the first line of a comment:
//
//	[pipeline:v1:<kind>] key=value key2="value two"
//
// Values containing spaces, quotes, backslashes or newlines are quoted with
// backslash escapes; everything else is bare. Prose for humans goes on the
// lines below the marker and is never parsed.
package marker

import (
	"fmt"
	"sort"
	"strings"
)

// Version is the grammar version this binary reads and writes. A parser
// meeting a different version fails loudly: guessing at a future grammar is
// how a count silently drifts.
const Version = "v1"

// Kind names what a marker records. New kinds may be added within a
// version; the parser accepts unknown kinds so that an old binary skips
// rather than chokes on a newer comment.
type Kind string

const (
	// Dispatch records an agent run's claim on a ticket (DESIGN §6).
	Dispatch Kind = "dispatch"
	// CIRed records one CI failure on the ticket's branch; two on the same
	// branch escalate to Blocked (DESIGN §12).
	CIRed Kind = "ci-red"
	// ReconcileBounce records one reconciliation failure; the second on the
	// same ticket escalates to Blocked (DESIGN §12).
	ReconcileBounce Kind = "reconcile-bounce"
	// BoundaryStep records a completed boundary-agent step; resume starts at
	// the first step without one (DESIGN §10).
	BoundaryStep Kind = "boundary-step"
	// DecisionlessPass records a design pass that found nothing to
	// approve — no screens, no artifacts, no systems/*.md diff — which
	// advances the ticket directly to Ready for dev (DESIGN §3, §6, §9).
	DecisionlessPass Kind = "decisionless-pass"
	// Revert records the control plane undoing an invariant-violating
	// transition (DESIGN §9).
	Revert Kind = "revert"
	// StaleClaim records a ticket moved to Blocked because its claiming run
	// died (DESIGN §12).
	StaleClaim Kind = "stale-claim"
	// TriageProposal carries the dedupe key that keeps a re-run boundary
	// pass from filing a finding twice (DESIGN §10).
	TriageProposal Kind = "triage-proposal"
	// Composition records the boundary's proposed contents for the next
	// debt milestone: all gating debt plus the non-gating floor
	// (DESIGN §10). Carries the ticket keys so accepting it can enact
	// exactly the reviewed list rather than whatever is selected.
	Composition Kind = "composition"
	// Merged records the merge commit on the ticket. The post-deploy
	// check reads it to compare against the platform's deployment
	// (DESIGN §13) — without it, "is this deployed" has no left-hand side.
	Merged Kind = "merged"
	// LiveSuite records the milestone live-suite result on the boundary
	// ticket — the once-per-milestone real-network check the author's
	// pass reads (DESIGN §10). Fields: result=pass|fail, run=<url>.
	LiveSuite Kind = "live-suite"
	// HarnessFinding records a problem an agent hit in the pipeline
	// itself — a check that was vacuous, a credential that was missing,
	// a protocol with no route for what the run met. Agents noticed
	// these constantly and had nowhere to put them: the observations
	// went into hand-back prose, and the boundary scan's bounded inputs
	// (DESIGN §10) do not include hand-backs, so the one channel for
	// "the harness is broken" reached nobody who could fix it. Narrow on
	// purpose — product debt keeps the boundary's own scan, because an
	// agent filing whatever it noticed is how a backlog stops being
	// trusted (DESIGN §4). Fields: id=<dedupe>, title=<one line>.
	HarnessFinding Kind = "harness-finding"
	// Base records the merge-base the design was drawn against
	// (DESIGN §2.4, §4). The description names it as the place this
	// lives, and nothing ever wrote it there: descriptions are the
	// immutable original argument (§2.3), so the only writer that could
	// have filled it in is the one forbidden to edit it. So the base
	// check — optimistic concurrency control, checked at pickup — was
	// documentation of a check that never ran, and every dev hand-back
	// spent a paragraph saying the sha was absent. A marker instead: the
	// harness posts it at design finish, when the value first exists,
	// and the description stays untouched. Field: sha=<merge-base>.
	Base Kind = "base"
	// Blocked records a ticket's arrival in Blocked and, in `from`, the
	// state it arrived from. Only the author moves a ticket out of
	// Blocked and they choose the state (DESIGN §12) — which is right,
	// because unblocking usually needs something the automation cannot
	// do. But choosing needs knowing where it came from, and until this
	// existed only the stale-claim path recorded that; everywhere else
	// the origin lived in the tracker's history and nowhere a tool could
	// read. Carried on whichever marker the transition already posts,
	// and posted alone when it posts none. Fields: from=<state>, and on
	// a needs-setup block, setup=<what a human has to do>.
	Blocked Kind = "blocked"
	// Preview records where this design pass's storybook export was
	// published. Design review is the author reading the rendered
	// states (DESIGN §4), and until this existed the ticket asking for
	// that review never said where to find them — the URL had to be
	// reconstructed from a branch name and a Pages project. Field:
	// url=<the published preview>.
	Preview Kind = "preview"
)

// Marker is one parsed or to-be-formatted marker line.
type Marker struct {
	Kind   Kind
	Fields map[string]string
}

const prefix = "[pipeline:"

// Format renders the marker line. Keys are sorted so output is
// deterministic — markers get compared in tests and deduped by content.
func (m Marker) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s:%s]", prefix, Version, m.Kind)
	keys := make([]string, 0, len(m.Fields))
	for k := range m.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteByte(' ')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(quote(m.Fields[k]))
	}
	return b.String()
}

// Comment renders a full comment body: the marker line, then prose for
// humans. The prose is display only; nothing may parse it (DESIGN §9).
func (m Marker) Comment(prose string) string {
	if prose == "" {
		return m.Format()
	}
	return m.Format() + "\n\n" + prose
}

func quote(v string) string {
	if v != "" && !strings.ContainsAny(v, " \"\\\n\t") {
		return v
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Parse reads a marker from a comment body. Returns ok=false when the body
// is not a marker comment at all (human prose); returns an error when it is
// unmistakably a marker but unreadable — wrong version, bad syntax — because
// skipping a malformed marker silently would corrupt whatever count or
// resume depends on it.
func Parse(body string) (Marker, bool, error) {
	line, _, _ := strings.Cut(body, "\n")
	line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
	if !strings.HasPrefix(line, prefix) {
		return Marker{}, false, nil
	}
	head, rest, found := strings.Cut(line[len(prefix):], "]")
	if !found {
		return Marker{}, false, fmt.Errorf("marker: unterminated header in %q", line)
	}
	ver, kind, found := strings.Cut(head, ":")
	if !found || kind == "" {
		return Marker{}, false, fmt.Errorf("marker: header %q is not version:kind", head)
	}
	if ver != Version {
		return Marker{}, false, fmt.Errorf("marker: version %q, this binary reads %q", ver, Version)
	}
	fields, err := parseFields(rest)
	if err != nil {
		return Marker{}, false, fmt.Errorf("marker: %w in %q", err, line)
	}
	return Marker{Kind: Kind(kind), Fields: fields}, true, nil
}

func parseFields(s string) (map[string]string, error) {
	fields := map[string]string{}
	i := 0
	for {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			return fields, nil
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq <= 0 {
			return nil, fmt.Errorf("field without key=value at %q", s[i:])
		}
		key := s[i : i+eq]
		if strings.ContainsAny(key, " \"\\") {
			return nil, fmt.Errorf("malformed key %q", key)
		}
		if _, dup := fields[key]; dup {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		i += eq + 1
		val, next, err := parseValue(s, i)
		if err != nil {
			return nil, err
		}
		fields[key] = val
		i = next
	}
}

func parseValue(s string, i int) (string, int, error) {
	if i < len(s) && s[i] == '"' {
		var b strings.Builder
		i++
		for i < len(s) {
			switch s[i] {
			case '\\':
				if i+1 >= len(s) {
					return "", 0, fmt.Errorf("dangling escape")
				}
				switch s[i+1] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case '"', '\\':
					b.WriteByte(s[i+1])
				default:
					return "", 0, fmt.Errorf("unknown escape \\%c", s[i+1])
				}
				i += 2
			case '"':
				return b.String(), i + 1, nil
			default:
				b.WriteByte(s[i])
				i++
			}
		}
		return "", 0, fmt.Errorf("unterminated quoted value")
	}
	end := strings.IndexByte(s[i:], ' ')
	if end < 0 {
		return s[i:], len(s), nil
	}
	return s[i : i+end], i + end, nil
}
