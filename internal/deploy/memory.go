package deploy

import (
	"context"
	"sync"
)

// Memory is a settable fake: tests script what the platform reports.
type Memory struct {
	mu      sync.Mutex
	current *State
	err     error
}

var _ Deploy = (*Memory)(nil)

func NewMemory() *Memory { return &Memory{} }

// Set scripts the next answer. Nil state means "platform reports nothing".
func (m *Memory) Set(s *State, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.current, m.err = s, err
}

func (m *Memory) State(_ context.Context) (*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	if m.current == nil {
		return &State{}, nil
	}
	s := *m.current
	return &s, nil
}
