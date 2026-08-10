package host

import (
	"context"
	"sync"
)

// Memory records dispatches for tests to assert on.
type Memory struct {
	mu         sync.Mutex
	Dispatches []Dispatch
}

var _ Host = (*Memory)(nil)

func NewMemory() *Memory { return &Memory{} }

func (m *Memory) DispatchWorkflow(_ context.Context, d Dispatch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Dispatches = append(m.Dispatches, d)
	return nil
}
