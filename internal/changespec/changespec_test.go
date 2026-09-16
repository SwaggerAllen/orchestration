package changespec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHeaderRoundTrips(t *testing.T) {
	for _, tc := range []struct{ key, title string }{
		{"ORC-181", "Give the work surface the protocol it needs"},
		{"ORC-7", "A title with — an em-dash inside it"},
		{"ABC1-42", ""},
	} {
		key, title, ok := ParseHeader([]byte(Header(tc.key, tc.title)))
		if !ok {
			t.Fatalf("Header(%q,%q) does not parse", tc.key, tc.title)
		}
		if key != tc.key || title != tc.title {
			t.Errorf("round trip of (%q,%q) gave (%q,%q)", tc.key, tc.title, key, title)
		}
	}
}

// The separator is decoration and the key is the rule, so every shape a
// model plausibly writes has to parse. A gate that failed on an en-dash
// would be failing on something that is not the rule.
func TestParseHeaderAcceptsEverySeparatorAModelWrites(t *testing.T) {
	for _, line := range []string{
		"# ORC-181 — Give the work surface the protocol it needs",
		"# ORC-181 – Give the work surface the protocol it needs",
		"# ORC-181 - Give the work surface the protocol it needs",
		"# ORC-181 Give the work surface the protocol it needs",
		"#\tORC-181 — Give the work surface the protocol it needs",
		"# ORC-181 — Give the work surface the protocol it needs\r",
		"\xef\xbb\xbf# ORC-181 — Give the work surface the protocol it needs",
	} {
		key, title, ok := ParseHeader([]byte(line + "\n\nbody\n"))
		if !ok {
			t.Errorf("did not parse: %q", line)
			continue
		}
		if key != "ORC-181" {
			t.Errorf("%q: key %q", line, key)
		}
		if title != "Give the work surface the protocol it needs" {
			t.Errorf("%q: title %q", line, title)
		}
	}
}

func TestParseHeaderRefusesWhatIsNotAHeader(t *testing.T) {
	for _, line := range []string{
		"ORC-181 — no heading marker",
		"## ORC-181 — the wrong level",
		"# orc-181 — lowercased",
		"# — no key at all",
		"#ORC-181 — no space after the hash",
		"",
	} {
		if _, _, ok := ParseHeader([]byte(line + "\n")); ok {
			t.Errorf("parsed what is not a header: %q", line)
		}
	}
}

func TestVerifyPasses(t *testing.T) {
	if err := Verify([]byte(Header("ORC-181", "t")+"\n\nbody\n"), "ORC-181"); err != nil {
		t.Fatalf("matching key: %v", err)
	}
}

// The failure this catches is a merge resolved the wrong way: `merge=ours`
// keeps the branch's copy silently, so a spec naming another ticket is the
// only observable, and it is only an observable if somebody looks.
func TestVerifyNamesBothKeysOnAMismatch(t *testing.T) {
	err := Verify([]byte(Header("ORC-235", "someone else's")+"\n"), "ORC-181")
	if err == nil {
		t.Fatal("a spec naming another ticket passed")
	}
	for _, want := range []string{"ORC-235", "ORC-181"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not name %s", err, want)
		}
	}
}

func TestVerifyQuotesTheLineItRejected(t *testing.T) {
	err := Verify([]byte("Some prose nobody meant as a header\n"), "ORC-181")
	if err == nil {
		t.Fatal("prose passed as a header")
	}
	if !strings.Contains(err.Error(), "Some prose nobody meant as a header") {
		t.Errorf("message does not quote the offending line: %q", err)
	}
}

func TestVerifySeparatesBlankFromMalformed(t *testing.T) {
	blank := Verify([]byte("\n\n# ORC-181 — buried\n"), "ORC-181")
	if blank == nil || !strings.Contains(blank.Error(), "blank") {
		t.Errorf("a blank first line should say so: %v", blank)
	}
}

// A design pass that never wrote the file and a dev pass that deleted one
// are different mistakes. Collapsed into one message, the second sends its
// reader looking for a malformed header that is not there.
func TestVerifyFileSeparatesMissingFromMalformed(t *testing.T) {
	dir := t.TempDir()
	missing := VerifyFile(dir, "ORC-181")
	if missing == nil || !strings.Contains(missing.Error(), "not found") {
		t.Fatalf("a missing file should say so: %v", missing)
	}

	if err := os.WriteFile(filepath.Join(dir, Name), []byte("nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	malformed := VerifyFile(dir, "ORC-181")
	if malformed == nil || strings.Contains(malformed.Error(), "not found") {
		t.Fatalf("a malformed file should not read as missing: %v", malformed)
	}
}

func TestVerifyFilePasses(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, Name), []byte(Header("ORC-181", "t")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(dir, "ORC-181"); err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}
}
