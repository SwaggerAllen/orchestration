package host

import (
	"context"
	"fmt"
	"sync"
)

// Memory is the scriptable fake for tests.
type Memory struct {
	mu         sync.Mutex
	Dispatches []Dispatch
	Runs       []AgentRun
	PRs        []PR
	CheckState map[string]Checks // headSHA -> checks
	nextPR     int
}

// Dispatch records one DispatchWorkflow call.
type Dispatch struct {
	Workflow string
	Inputs   map[string]string
}

var _ Host = (*Memory)(nil)

func NewMemory() *Memory {
	return &Memory{CheckState: map[string]Checks{}}
}

func (m *Memory) DispatchWorkflow(_ context.Context, workflowFile string, inputs map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Dispatches = append(m.Dispatches, Dispatch{Workflow: workflowFile, Inputs: inputs})
	return nil
}

func (m *Memory) ListAgentRuns(_ context.Context) ([]AgentRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AgentRun, len(m.Runs))
	copy(out, m.Runs)
	return out, nil
}

func (m *Memory) ListOpenPRs(_ context.Context) ([]PR, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PR, len(m.PRs))
	copy(out, m.PRs)
	return out, nil
}

func (m *Memory) ChecksFor(_ context.Context, headSHA string) (Checks, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.CheckState[headSHA], nil
}

func (m *Memory) CreatePR(_ context.Context, branch, title, _ string, draft bool) (PR, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.PRs {
		if p.Branch == branch {
			return PR{}, fmt.Errorf("memory host: PR for branch %q already exists", branch)
		}
	}
	m.nextPR++
	pr := PR{Number: m.nextPR, Branch: branch, HeadSHA: fmt.Sprintf("sha_%d", m.nextPR), Draft: draft}
	m.PRs = append(m.PRs, pr)
	return pr, nil
}

func (m *Memory) MarkPRReady(_ context.Context, number int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.PRs {
		if m.PRs[i].Number == number {
			m.PRs[i].Draft = false
			return nil
		}
	}
	return fmt.Errorf("memory host: no PR #%d", number)
}
