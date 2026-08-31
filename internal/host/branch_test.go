package host

import "testing"

// The token rule, and why it is not a Contains: `orc-1` is a prefix of
// `orc-19` and `orc-181`, so a substring match routes ORC-19's PR and
// its merge commit to ORC-1. With three digits on the board that is the
// common case rather than a corner.
func TestBranchBelongsToMatchesWholeKeysOnly(t *testing.T) {
	for _, c := range []struct {
		branch, key string
		want        bool
	}{
		{"orc-199-backfill-the-merged-marker", "ORC-199", true},
		{"ORC-199", "orc-199", true},
		{"andrewvickory/orc-171-runtime-position", "ORC-171", true},
		{"claude/orc-42", "ORC-42", true},
		// The near misses.
		{"orc-199-backfill", "ORC-1", false},
		{"orc-1990-something", "ORC-199", false},
		{"orc-19", "ORC-199", false},
		{"xorc-199", "ORC-199", false},
		{"feature/no-key-here", "ORC-199", false},
		{"orc-199", "", false},
	} {
		if got := BranchBelongsTo(c.branch, c.key); got != c.want {
			t.Errorf("BranchBelongsTo(%q, %q) = %v, want %v", c.branch, c.key, got, c.want)
		}
	}
}

// A PR closed without merging has no commit, so it must not be reported
// as one. Reading "closed" as "merged" is the status-is-not-conclusion
// mistake in another costume.
func TestMemoryReportsOnlyThisTicketsMerges(t *testing.T) {
	m := NewMemory()
	m.MergedPRs = []MergedPR{
		{Number: 1, Branch: "orc-199-a", MergeSHA: "first"},
		{Number: 2, Branch: "orc-200-b", MergeSHA: "other-ticket"},
		{Number: 3, Branch: "orc-199-a-second-time", MergeSHA: "second"},
	}
	got, err := m.MergedPRsFor(t.Context(), "ORC-199")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].MergeSHA != "first" || got[1].MergeSHA != "second" {
		t.Fatalf("got %+v, want ORC-199's two merges, oldest first", got)
	}
}

// MergePR is the other way merges get recorded, and it has to leave the
// same trail: branch kept, and the PR gone from the open list, as on the
// real host where a merged PR is closed.
func TestMergePRRecordsTheBranchAndClosesThePR(t *testing.T) {
	ctx := t.Context()
	m := NewMemory()
	pr, err := m.CreatePR(ctx, "orc-199-through-the-pipeline", "t", "b", false)
	if err != nil {
		t.Fatal(err)
	}
	sha, err := m.MergePR(ctx, pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	open, err := m.ListOpenPRs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Errorf("a merged PR is still in the open list: %+v", open)
	}
	got, err := m.MergedPRsFor(ctx, "ORC-199")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MergeSHA != sha || got[0].Branch != "orc-199-through-the-pipeline" {
		t.Errorf("got %+v, want the merge MergePR just made", got)
	}
}
