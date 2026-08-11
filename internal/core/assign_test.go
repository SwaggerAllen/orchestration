package core

import (
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// The assignment table IS the state table's "who has it" column. If a
// state moves columns, this test is what notices.
func TestAssignmentMirrorsTheBall(t *testing.T) {
	authorHeld := map[protocol.State]bool{
		protocol.DesignReview:   true,
		protocol.Blocked:        true,
		protocol.BoundaryReview: true,
	}
	for _, st := range protocol.AllStates {
		s := snap(tk("T1", st))
		s.AuthorID = "usr_author"
		acts := Sweep(s)
		var got *Action
		for i := range acts {
			if acts[i].Kind == ActAssign {
				got = &acts[i]
			}
		}
		if authorHeld[st] {
			if got == nil || got.Assignee != "usr_author" {
				t.Errorf("state %q: want assign to author, got %v", st, got)
			}
			continue
		}
		// Unassigning an already-unassigned ticket plans nothing.
		if got != nil {
			t.Errorf("state %q: want no assignment (already unassigned), got %v", st, *got)
		}
	}
}

func TestAssignmentUnassignsWhenTheBallMovesOn(t *testing.T) {
	ticket := tk("T1", protocol.ReadyForDev, arrived(protocol.DesignReview, RoleAuthor))
	ticket.AssigneeID = "usr_author" // left over from Design review
	s := snap(ticket)
	s.AuthorID = "usr_author"
	a := find(Sweep(s), ActAssign, "T1")
	if a == nil || a.Assignee != "" {
		t.Errorf("want unassign once the queue holds it, got %v", a)
	}
}

func TestBoundaryTodoIsTheAuthorsPass(t *testing.T) {
	b := tk("B1", protocol.Todo, func(t *Ticket) { t.Labels = []string{LabelBoundary} })
	s := snap(b)
	s.AuthorID = "usr_author"
	a := find(Sweep(s), ActAssign, "B1")
	if a == nil || a.Assignee != "usr_author" {
		t.Errorf("the boundary ticket's Todo is the author's manual pass (DESIGN 10), got %v", a)
	}

	// An ordinary Todo is nobody's.
	ord := tk("T2", protocol.Todo)
	ord.AssigneeID = "usr_author"
	s2 := snap(ord)
	s2.AuthorID = "usr_author"
	if a := find(Sweep(s2), ActAssign, "T2"); a == nil || a.Assignee != "" {
		t.Errorf("ordinary Todo must unassign, got %v", a)
	}
}

func TestAssignmentOffWithoutAuthorID(t *testing.T) {
	ticket := tk("T1", protocol.Blocked)
	s := snap(ticket) // AuthorID empty
	for _, a := range Sweep(s) {
		if a.Kind == ActAssign {
			t.Errorf("no author id configured: assignment must stay off, got %v", a)
		}
	}
}
