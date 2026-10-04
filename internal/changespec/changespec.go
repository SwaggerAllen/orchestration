// Package changespec is the grammar of the ticket spec file: one
// CHANGE.md at the repository root, overwritten wholly by each ticket's
// design pass, carrying the sketch DESIGN §4 specifies and otherwise has
// no file for.
//
// A parse/format module rather than a convention, for the reason PLAN §1
// gives about marker comments: two components communicate through this
// string — the design pass writes it, claim and finish read it back — and
// an unspecified string API is where the sim and production quietly
// diverge.
package changespec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Name is the file, at the repository root.
//
// One file rather than one per ticket. A per-ticket directory accumulates
// entries that were true when they merged and are evidence about nothing
// afterwards, and would need a file-map rule to keep passes from reading
// stale ones as authority; one overwritten file needs no such rule,
// because there is only ever one and it is the current one.
const Name = "CHANGE.md"

// headerRe matches line one: a level-one heading whose first word is the
// ticket key, and whatever follows as the title.
//
// **Strict on the key, lenient on the separator**, and the asymmetry is
// the point. The key is what every assertion here turns on, so it is
// anchored and shaped exactly as `design.go`'s finding ids already spell
// a ticket. The separator is decoration: requiring an em-dash would fail
// a pass that wrote a hyphen and read correctly to every human who saw
// it, which is a gate failing on something that is not the rule.
var headerRe = regexp.MustCompile(`^#[ \t]+([A-Z][A-Z0-9]*-[0-9]+)\b[ \t]*[—–-]?[ \t]*(.*?)[ \t]*$`)

// Header renders line one. The em-dash is the written form; ParseHeader
// accepts more than this produces, which is the ordinary direction for a
// grammar one side writes and the other reads.
func Header(key, title string) string {
	if title == "" {
		return "# " + key
	}
	return "# " + key + " — " + title
}

// ParseHeader reads the ticket key and title off the first line. A title
// is optional; a key is not.
func ParseHeader(content []byte) (key, title string, ok bool) {
	s := string(content)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	// A byte-order mark ahead of the heading is invisible in every editor
	// and would make the header unparseable for a reason nobody could see.
	s = strings.TrimPrefix(s, "\xef\xbb\xbf")
	m := headerRe.FindStringSubmatch(strings.TrimRight(s, "\r"))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// Verify reports whether content is a spec for want.
//
// Every failure names what it actually found, because this message is
// read by somebody who cannot see the file — the same rule DESIGN's own
// comments follow, and the defect `staleClaimFor` is on record for: a
// fixed string that states a diagnosis it did not check sends the reader
// hunting in the wrong place.
func Verify(content []byte, want string) error {
	key, _, ok := ParseHeader(content)
	if !ok {
		first := firstLine(content)
		if strings.TrimSpace(first) == "" {
			return fmt.Errorf("%s: first line is blank, wanted %q", Name, Header(want, "<title>"))
		}
		return fmt.Errorf("%s: first line %q is not a spec header, wanted %q", Name, first, Header(want, "<title>"))
	}
	if key != want {
		return fmt.Errorf("%s: names %s, but this run is working %s", Name, key, want)
	}
	return nil
}

// VerifyFile is Verify against the file in dir.
//
// A missing file is its own message rather than a parse failure: the
// design pass not having written one and a dev pass having deleted one
// are different mistakes, and collapsing them would send the second
// looking for a malformed header that is not there.
func VerifyFile(dir, want string) error {
	path := filepath.Join(dir, Name)
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: not found at the repository root; the design pass writes it and every pass keeps it", Name)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", Name, err)
	}
	return Verify(content, want)
}

func firstLine(content []byte) string {
	s := string(content)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, "\r")
}
