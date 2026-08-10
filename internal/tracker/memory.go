package tracker

import (
	"context"
	"fmt"
	"sync"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// Memory is the in-memory fake for Ring-1 and Ring-2 tests. It enforces the
// same constraints Linear does — unique names per team, a valid category on
// every state — because a fake looser than the real service passes tests the
// dry run then fails.
type Memory struct {
	mu     sync.Mutex
	nextID int
	states map[string][]StateInfo // teamID -> states
	labels map[string][]Label     // teamID -> labels
}

var _ Tracker = (*Memory)(nil)

func NewMemory() *Memory {
	return &Memory{
		states: map[string][]StateInfo{},
		labels: map[string][]Label{},
	}
}

func (m *Memory) id(kind string) string {
	m.nextID++
	return fmt.Sprintf("%s_%d", kind, m.nextID)
}

func (m *Memory) ListStates(_ context.Context, teamID string) ([]StateInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]StateInfo, len(m.states[teamID]))
	copy(out, m.states[teamID])
	return out, nil
}

func (m *Memory) CreateState(_ context.Context, teamID, name string, category protocol.Category) (StateInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if name == "" {
		return StateInfo{}, fmt.Errorf("memory tracker: state name is required")
	}
	switch category {
	case protocol.CategoryBacklog, protocol.CategoryUnstarted, protocol.CategoryStarted,
		protocol.CategoryCompleted, protocol.CategoryCanceled:
	default:
		return StateInfo{}, fmt.Errorf("memory tracker: invalid state category %q", category)
	}
	for _, s := range m.states[teamID] {
		if s.Name == name {
			return StateInfo{}, fmt.Errorf("memory tracker: state %q already exists in team %s", name, teamID)
		}
	}
	s := StateInfo{ID: m.id("state"), Name: name, Category: category}
	m.states[teamID] = append(m.states[teamID], s)
	return s, nil
}

func (m *Memory) ListLabels(_ context.Context, teamID string) ([]Label, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Label, len(m.labels[teamID]))
	copy(out, m.labels[teamID])
	return out, nil
}

func (m *Memory) CreateLabel(_ context.Context, teamID, name string) (Label, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if name == "" {
		return Label{}, fmt.Errorf("memory tracker: label name is required")
	}
	for _, l := range m.labels[teamID] {
		if l.Name == name {
			return Label{}, fmt.Errorf("memory tracker: label %q already exists in team %s", name, teamID)
		}
	}
	l := Label{ID: m.id("label"), Name: name}
	m.labels[teamID] = append(m.labels[teamID], l)
	return l, nil
}
