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
	// MergeConflict records a reconciliation that passed and could not
	// land: the branch conflicts with something that merged while this
	// ticket was in flight. Counted separately from ReconcileBounce on
	// purpose — that count escalates to Blocked because "two failures to
	// land the same scope is a design problem" (DESIGN §12), and a
	// conflict is not a finding about the work at all. It is a stale
	// branch, and the verdict that preceded it was PASS. Fields:
	// pr=<number>, attempt=<n>.
	MergeConflict Kind = "merge-conflict"
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
	// a needs-setup block, setup=<what a human has to do>. Also
	// pushed=<sha> when the run had already pushed the ticket branch
	// before it failed, so the ticket says whether the work survived
	// rather than leaving it to be read off the branch by hand. On a
	// marker-counted escalation, the count it fired at — bounces=<n> for
	// the second reconcile bounce, conflicts=<n> for the third merge
	// conflict — so a later sweep can tell its own escalation from a
	// fresh failure and the author's return to `Ready for rework`
	// stands (DESIGN §12). The escalation records it here rather than
	// under the kind it counts, which is what kept the conflict rule
	// re-reading its own output as a fourth conflict.
	Blocked Kind = "blocked"
	// Preview records this pass's preview environment. Design review is
	// the author reading the rendered states (DESIGN §4), and until this
	// existed the ticket asking for that review never said where to find
	// them — the URL had to be reconstructed from a branch name and a
	// Pages project.
	//
	// Fields: url=<where the preview is>, and state=failed on the one
	// that reports a preview that is not coming. A marker with no `state`
	// carries a URL and means ready — that is every marker written before
	// the field existed, and reading absent as ready is what keeps those
	// tickets legible.
	//
	// The url-bearing one is terminal: once a preview has been announced
	// there is nothing further to say, and that is the predicate the sweep
	// uses to stop looking. A `state=failed` marker is not terminal, so a
	// preview that fails and is later rebuilt still gets announced.
	Preview Kind = "preview"
	// ClaimFailed records a run that died before it held the ticket.
	//
	// Every other failure route posts through the claim: the abort path
	// needs claim.json to know what it is aborting, and a run that never
	// got one skips it. So the loudest failures — the harness broken
	// before the agent even started — were the only ones that left
	// nothing on the ticket at all. ORC-7 sat in Designing for 23
	// minutes with no comment, until the stale-claim rule moved it and
	// said only that a run had died.
	ClaimFailed Kind = "claim-failed"
	// RecordReview records the record review's verdict on a design
	// pass's doc diff (DESIGN §4). Fields: verdict=pass|decline. On a
	// decline the prose is the findings — which passages read as
	// narration of passes or rejected alternatives rather than as a
	// rule and its reason — and it reaches the next design pass through
	// the thread, the way the author's own decline does. The sweep
	// counts declines: the second on one ticket escalates to Blocked
	// (DESIGN §12), because a reviewer and a writer that disagree twice
	// running are not going to settle it on a third pass.
	RecordReview Kind = "record-review"
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

// Prose returns the part of a comment written for a human — the marker
// header removed — and whether the comment is worth putting in front of
// a model at all.
//
// The prompts render a ticket's whole comment list, markers included, and
// that was two problems in one. Bytes: a ticket that fails repeatedly
// grows its own prompt, because each failed run appends a dispatch marker
// and a blocked marker that the next claim inlines, on top of a prompt
// already large enough to have hit the argv cap. And signal: the control
// plane's notes to itself — a dispatch id, a merge-base sha, a Pages URL
// — read as context to a model that has no use for any of them.
//
// The rule is deliberately not "drop marker comments". Several carry an
// argument under the header, and they are the ones that matter most: a
// push-back's reason, a bounce's rework scope, a decisionless pass's
// summary. Dropping those would delete exactly the text a returning pass
// is supposed to implement.
//
// A body that is not a marker at all comes back untouched. That is the
// ordinary case — the author's comments and the agents' hand-backs.
func Prose(body string) (string, bool) {
	m, ok, err := Parse(body)
	if err != nil || !ok {
		// A malformed marker is kept rather than dropped. Parse is strict
		// because counts and resumes depend on it; this is a display
		// decision, and showing a model one odd line beats hiding a
		// comment somebody wrote.
		return body, strings.TrimSpace(body) != ""
	}
	if bookkeeping(m) {
		return "", false
	}
	_, rest, _ := strings.Cut(body, "\n")
	rest = strings.TrimSpace(rest)
	return rest, rest != ""
}

// bookkeeping reports whether a marker comment is the control plane
// talking about itself, with nothing under it a pass could act on.
//
// Kind alone does not decide it. `blocked` is posted for seven different
// arrivals (DESIGN §12): a push-back, a needs-setup, an author-only
// split, a scope-satisfied park and a prerequisite park all carry the
// argument that put the ticket there, while a plain failed run carries a
// URL and — since the harness started capturing what a run printed — up
// to forty lines of a CLI's death rattle. That last one on a prompt is
// worse than useless: it is the previous run's crash presented to the
// next run as context. So the flavor field decides, not the kind.
//
// A flavor missing from the list below is not a small mistake: its
// argument is dropped from the next run's prompt, so the pass that
// re-picks the ticket up is told only that it was blocked and never why
// — which is the whole failure this function exists to prevent, arriving
// through the one door nothing checks.
func bookkeeping(m Marker) bool {
	switch m.Kind {
	case Dispatch, Base, Preview, StaleClaim, ClaimFailed, Merged, Revert, BoundaryStep:
		return true
	case Blocked:
		for _, flavor := range []string{"pushback", "setup", "author-only", "scope-satisfied", "prerequisite", "live-suite", "conflict"} {
			if m.Fields[flavor] != "" {
				return false
			}
		}
		return true
	}
	// Everything else keeps its prose: ci-red and merge-conflict say what
	// went wrong, reconcile-bounce is the rework scope, decisionless-pass
	// and harness-finding and composition and live-suite are all somebody
	// or something making an argument.
	return false
}
