// Package promptdoc keeps a list in one place by letting DESIGN.md hold
// it and the prompts include it (DESIGN §13).
//
// A vocabulary the pipeline depends on could live in four places at
// once: DESIGN, a role prompt, the `switch` that enforces it, and a test
// that restated it. Nothing held any pair together, and two had already
// drifted when this was written. The debt scan's bounded inputs were
// five in DESIGN and four in the prompt — probably deliberate, since the
// fifth arrives as a generated section, but nothing said so, and the
// cost of a duplicate pair is exactly that: not that the copies
// disagree, but that a reader cannot tell whether a disagreement is a
// decision or a bug.
//
// Anchored markdown rather than a fenced block, because the point is to
// keep the content where the reader already looks. A fence would turn a
// paragraph of the protocol into config; HTML comments render as nothing
// and leave an ordinary list on the page.
package promptdoc

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Marker syntax. Both are HTML comments so they vanish in any renderer,
// and both are line-oriented so a block is a run of whole lines — the
// alternative, inline spans, would make an include's indentation depend
// on where it was written.
// Leading whitespace is allowed on all three, because an anchor has to
// work where the content belongs. DESIGN's debt-scan list is item 7 of a
// numbered list, so its lines carry three spaces; a pattern anchored to
// the hard start of the line matched the close and not the open, and the
// block came back absent rather than malformed — silently, since a
// missing id is indistinguishable from one nobody defined.
var (
	openRe    = regexp.MustCompile(`^\s*<!--\s*pipeline:list\s+id=([a-z0-9-]+)(\s+for=([a-z]+))?\s*-->\s*$`)
	closeRe   = regexp.MustCompile(`^\s*<!--\s*/pipeline:list\s*-->\s*$`)
	includeRe = regexp.MustCompile(`(?m)^[ \t]*<!--\s*pipeline:include\s+([a-z0-9-]+)\s*(\([^)]*\))?\s*-->[ \t]*$`)
)

// Block is one anchored list.
type Block struct {
	Body string
	// Reference marks a block no prompt is meant to include: it exists
	// so the protocol states something once and a test can hold the code
	// to it. §13's outcome table is the case — eighteen rows spanning
	// four emitters, where a design pass needs three of them, so
	// inlining it everywhere would be the context bloat this repo keeps
	// removing. Written `for=tests` on the anchor.
	//
	// The distinction has to be in the document rather than in a list
	// inside the checker, or the checker grows the copy this whole
	// mechanism exists to delete.
	Reference bool
}

// Blocks reads the anchored lists out of a document, keyed by id.
//
// Duplicate ids, an unclosed block and a close with no open are all
// errors rather than best-effort recoveries. This runs in CI on every
// change to either file, so a malformed anchor is a build failure at the
// moment somebody wrote it — which is the only moment it is cheap.
func Blocks(doc string) (map[string]Block, error) {
	out := map[string]Block{}
	var id string
	var reference bool
	var buf []string
	for n, line := range strings.Split(doc, "\n") {
		if m := openRe.FindStringSubmatch(line); m != nil {
			if id != "" {
				return nil, fmt.Errorf("line %d: pipeline:list %q opened inside %q", n+1, m[1], id)
			}
			if _, dup := out[m[1]]; dup {
				return nil, fmt.Errorf("line %d: pipeline:list %q is defined twice — an include would have to guess", n+1, m[1])
			}
			id, reference, buf = m[1], m[3] == "tests", nil
			continue
		}
		if closeRe.MatchString(line) {
			if id == "" {
				return nil, fmt.Errorf("line %d: /pipeline:list closes nothing", n+1)
			}
			body := dedent(strings.Trim(strings.Join(buf, "\n"), "\n"))
			if strings.TrimSpace(body) == "" {
				// An empty block is the failure this package exists to
				// prevent, arriving early. An empty bounded-input list
				// turns the debt scan into the open-ended question that
				// DESIGN §10 says produces invented findings.
				return nil, fmt.Errorf("line %d: pipeline:list %q is empty", n+1, id)
			}
			out[id] = Block{Body: body, Reference: reference}
			id, reference = "", false
			continue
		}
		if id != "" {
			buf = append(buf, line)
		}
	}
	if id != "" {
		return nil, fmt.Errorf("pipeline:list %q is never closed", id)
	}
	return out, nil
}

// Expand substitutes every include in a prompt with its block.
//
// Fails closed, and that is the whole contract. An unresolved include
// that rendered as nothing would delete a rule from a prompt silently,
// and a pass missing a rule does not report that it is missing one — it
// just does the wrong thing and says it went fine. A run that dies
// naming the anchor costs one dispatch; a prompt quietly short a
// section costs a pass nobody audits.
func Expand(prompt string, blocks map[string]Block) (string, error) {
	var missing []string
	out := includeRe.ReplaceAllStringFunc(prompt, func(line string) string {
		m := includeRe.FindStringSubmatch(line)
		b, ok := blocks[m[1]]
		if !ok {
			missing = append(missing, m[1])
			return line
		}
		body := b.Body
		// The trail travels into the assembled prompt, not just the
		// prompt file: whoever reads a run's prompt — a person debugging
		// it, or the model itself — can see the list came from the
		// protocol rather than from whoever wrote this template.
		if where := strings.TrimSpace(m[2]); where != "" {
			return body + "\n\n_" + strings.Trim(where, "()") + "_"
		}
		return body
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", fmt.Errorf("prompt includes %s, which DESIGN.md does not define",
			strings.Join(quoted(missing), ", "))
	}
	return out, nil
}

// Includes names the anchors a prompt asks for, so a CI check can hold
// the two files together without expanding anything.
func Includes(prompt string) []string {
	var out []string
	for _, m := range includeRe.FindAllStringSubmatch(prompt, -1) {
		out = append(out, m[1])
	}
	return out
}

func quoted(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

// dedent strips the block's common leading whitespace.
//
// Anchors have to work where the content belongs, and in DESIGN that is
// often inside a numbered step — §10's debt scan is item 7 of a list, so
// its bullets are indented three spaces. Carried into a prompt verbatim
// those three spaces read as a code block to anything rendering markdown,
// which would put the bounded-input list in a grey box and change what it
// looks like it is. Stripping the common prefix keeps the block's own
// internal structure, including nested bullets, and only removes the
// indentation that came from where it was written.
func dedent(body string) string {
	lines := strings.Split(body, "\n")
	prefix := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue // a blank line says nothing about indentation
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if prefix < 0 || n < prefix {
			prefix = n
		}
	}
	if prefix <= 0 {
		return body
	}
	for i, l := range lines {
		if len(l) >= prefix {
			lines[i] = l[prefix:]
		} else {
			lines[i] = strings.TrimLeft(l, " \t")
		}
	}
	return strings.Join(lines, "\n")
}
