package deploy

import (
	"context"
	"sync"
)

// Memory is a settable fake: tests script what the platform reports.
type Memory struct {
	mu      sync.Mutex
	current *Deployment
	err     error
}

var _ Deploy = (*Memory)(nil)

func NewMemory() *Memory { return &Memory{} }

// Set scripts the next answer. A nil deployment means "nothing active".
func (m *Memory) Set(d *Deployment, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.current, m.err = d, err
}

func (m *Memory) ActiveDeployment(_ context.Context) (*Deployment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	if m.current == nil {
		return nil, nil
	}
	d := *m.current
	return &d, nil
}
