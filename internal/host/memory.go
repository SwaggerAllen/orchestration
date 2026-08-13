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
	// Merged records merged PRs: number -> merge SHA.
	Merged map[int]string
	// Ancestry scripts IsAncestor: "ancestor..descendant" -> true.
	// Identical SHAs are always ancestors, as in git.
	Ancestry map[string]bool
	// Files holds PutFileIfAbsent writes: path -> content.
	Files map[string]string
	// Deployments records RecordDeployment calls, in order.
	Deployments []Deployment
	nextPR      int
}

// Deployment is one recorded deployment.
type Deployment struct {
	SHA         string
	Environment string
}

// Dispatch records one DispatchWorkflow call.
type Dispatch struct {
	Workflow string
	Inputs   map[string]string
}

var _ Host = (*Memory)(nil)

func NewMemory() *Memory {
	return &Memory{
		CheckState: map[string]Checks{},
		Merged:     map[int]string{},
		Ancestry:   map[string]bool{},
		Files:      map[string]string{},
	}
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

func (m *Memory) MergePR(_ context.Context, number int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, p := range m.PRs {
		if p.Number == number {
			sha := fmt.Sprintf("merge_%d", number)
			m.Merged[number] = sha
			m.PRs = append(m.PRs[:i], m.PRs[i+1:]...)
			return sha, nil
		}
	}
	return "", fmt.Errorf("memory host: no open PR #%d to merge", number)
}

func (m *Memory) IsAncestor(_ context.Context, ancestor, descendant string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ancestor == descendant {
		return true, nil
	}
	return m.Ancestry[ancestor+".."+descendant], nil
}

// RecordDeployment records what a real host would create, so a Ring-1
// test can assert reconcile did it.
func (m *Memory) RecordDeployment(_ context.Context, sha, environment string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Deployments = append(m.Deployments, Deployment{SHA: sha, Environment: environment})
	return nil
}

func (m *Memory) PutFileIfAbsent(_ context.Context, path, content, _ string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.Files[path]; exists {
		return false, nil
	}
	m.Files[path] = content
	return true, nil
}
