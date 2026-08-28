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
	// DispatchErr makes DispatchWorkflow fail, standing in for the host
	// being briefly unreachable — the case where a reservation was taken
	// for a run that then never started.
	DispatchErr error
	Runs        []AgentRun
	PRs         []PR
	CheckState  map[string]Checks // headSHA -> checks
	// JobLogs scripts FailedJobLogs: headSHA -> failing jobs and their tails.
	JobLogs map[string][]JobLog
	// Merged records merged PRs: number -> merge SHA.
	Merged map[int]string
	// Ancestry scripts IsAncestor: "ancestor..descendant" -> true.
	// Identical SHAs are always ancestors, as in git.
	Ancestry map[string]bool
	// Files holds PutFile writes: path -> content.
	Files map[string]string
	// Deployments records RecordDeployment calls, in order.
	Deployments []Deployment
	// Unmergeable are PR numbers MergePR refuses with ErrNotMergeable,
	// standing in for a branch that conflicts with its base.
	Unmergeable map[int]bool
	// FailRecordDeployment makes RecordDeployment error, standing in for
	// the 403 a workflow without deployments: write actually gets.
	FailRecordDeployment bool
	// FailChecksFor makes the verdict read error for one head SHA,
	// standing in for the 403 that took a whole snapshot down. Keyed by
	// SHA rather than a flag, because the property worth testing is that
	// *one* ticket's unreadable verdict costs only that ticket.
	FailChecksFor map[string]bool
	// Reruns records RerunRun calls in order, so a test can assert the
	// harness asked — separately from asserting the attempt moved.
	Reruns []int64
	nextPR int
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
		CheckState:    map[string]Checks{},
		JobLogs:       map[string][]JobLog{},
		Merged:        map[int]string{},
		Ancestry:      map[string]bool{},
		Files:         map[string]string{},
		Unmergeable:   map[int]bool{},
		FailChecksFor: map[string]bool{},
	}
}

func (m *Memory) DispatchWorkflow(_ context.Context, workflowFile string, inputs map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DispatchErr != nil {
		return m.DispatchErr
	}
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
	if m.FailChecksFor[headSHA] {
		return Checks{}, fmt.Errorf("memory host: checks for %s are refused (HTTP 403)", headSHA)
	}
	return m.CheckState[headSHA], nil
}

// RerunRun models what the real re-run does to a verdict, which is the
// only part callers can observe: the run keeps its id and its URL, its
// attempt increments, and it stops being completed until the new
// attempt finishes. A fake that merely recorded the call would let a
// caller "confirm" a re-run by asking a question whose answer never
// changes — and confirming the attempt moved is the whole point, since
// a re-run that returns 201 and starts nothing is the failure worth
// catching (`internal/host`.Host.RerunRun).
//
// Unknown run ids are refused rather than ignored: asking to re-run
// something that is not there is a caller bug, and a fake that shrugs
// hides it.
func (m *Memory) RerunRun(_ context.Context, runID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for sha, c := range m.CheckState {
		if c.RunID != runID {
			continue
		}
		c.RunAttempt++
		c.Status = ChecksPending
		m.CheckState[sha] = c
		m.Reruns = append(m.Reruns, runID)
		return nil
	}
	return fmt.Errorf("memory host: no run %d to re-run", runID)
}

// MergeStateFor answers from the same Unmergeable set MergePR refuses
// from, so a fake cannot be set up to report a clean merge and then
// refuse one — the state the sweep reads and the state the merge hits
// are one fact.
func (m *Memory) MergeStateFor(_ context.Context, number int) (MergeState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Unmergeable[number] {
		return MergeConflicted, nil
	}
	return MergeClean, nil
}

func (m *Memory) FailedJobLogs(_ context.Context, headSHA string) ([]JobLog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.JobLogs[headSHA], nil
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
	if m.Unmergeable[number] {
		return "", fmt.Errorf("%w: PR #%d", ErrNotMergeable, number)
	}
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
	if m.FailRecordDeployment {
		return fmt.Errorf("memory host: deployments are refused (HTTP 403)")
	}
	m.Deployments = append(m.Deployments, Deployment{SHA: sha, Environment: environment})
	return nil
}

func (m *Memory) ReadFile(_ context.Context, path string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.Files[path]
	return c, ok, nil
}

func (m *Memory) PutFile(_ context.Context, path, content, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Files[path] = content
	return nil
}
