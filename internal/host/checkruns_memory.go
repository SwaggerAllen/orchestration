package host

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// MemoryCheckRuns is a settable fake for the check-runs port. Separate
// from Memory for the reason CheckRuns is separate from Host: a test that
// reaches check runs through the plane's host is a test asserting
// something production cannot do.
type MemoryCheckRuns struct {
	mu sync.Mutex
	// Runs are the recorded verdicts, in the order they were created.
	Runs []CheckRun
	// FailRead and FailCreate stand in for the 403 a token without
	// `checks: write` gets.
	FailRead, FailCreate bool
}

var _ CheckRuns = (*MemoryCheckRuns)(nil)

func NewMemoryCheckRuns() *MemoryCheckRuns { return &MemoryCheckRuns{} }

func (m *MemoryCheckRuns) CheckRunsFor(_ context.Context, sha, prefix string) ([]CheckRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailRead {
		return nil, fmt.Errorf("host: reading check runs for %s", sha)
	}
	var out []CheckRun
	for _, r := range m.Runs {
		if r.SHA == sha && strings.HasPrefix(r.Name, prefix) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *MemoryCheckRuns) CreateCheckRun(_ context.Context, cr CheckRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailCreate {
		return fmt.Errorf("host: recording check run %q", cr.Name)
	}
	m.Runs = append(m.Runs, cr)
	return nil
}
