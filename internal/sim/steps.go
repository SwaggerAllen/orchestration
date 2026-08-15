package sim

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/marker"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// step dispatches one scripted step. Unknown kinds fail loudly: a scenario
// written for a newer harness must not silently pass on an older one.
func (w *World) step(raw json.RawMessage) error {
	var head struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return err
	}
	switch head.Kind {
	case "seed":
		return w.stepSeed(raw)
	case "transition":
		return w.stepTransition(raw)
	case "comment":
		return w.stepComment(raw)
	case "label":
		return w.stepLabel(raw)
	case "ci":
		return w.stepCI(raw)
	case "deploy":
		return w.stepDeploy(raw)
	case "run-end":
		return w.stepRunEnd(raw)
	case "advance":
		return w.stepAdvance(raw)
	case "milestone":
		return w.stepMilestone(raw)
	case "kill":
		return w.stepKill(raw)
	case "sweep":
		return w.converge()
	case "expect":
		return w.stepExpect(raw)
	case "expect-boundary":
		return w.stepExpectBoundary(raw)
	default:
		return fmt.Errorf("unknown step kind %q", head.Kind)
	}
}

func parseState(s string) (protocol.State, error) {
	for _, st := range protocol.AllStates {
		if string(st) == s {
			return st, nil
		}
	}
	return "", fmt.Errorf("unknown state %q", s)
}

var roles = map[string]core.Role{
	"author": core.RoleAuthor, "design": core.RoleDesign, "dev": core.RoleDev,
	"reconcile": core.RoleReconcile, "boundary": core.RoleBoundary,
	"controlplane": core.RoleControlPlane, "ci": core.RoleCI, "other": core.RoleOther,
}

func parseRole(s string) (core.Role, error) {
	r, ok := roles[s]
	if !ok {
		return "", fmt.Errorf("unknown role %q", s)
	}
	return r, nil
}

var agentKinds = map[string]core.AgentKind{
	"design": core.AgentDesign, "dev": core.AgentDev,
	"reconcile": core.AgentReconcile, "boundary": core.AgentBoundary,
	"live-suite": core.AgentLiveSuite,
}

func (w *World) stepSeed(raw json.RawMessage) error {
	var p struct {
		Key        string   `json:"key"`
		State      string   `json:"state"`
		Labels     []string `json:"labels"`
		Priority   int      `json:"priority"`
		Milestone  string   `json:"milestone"`
		Blocks     []string `json:"blocks"`
		BlockedBy  []string `json:"blockedBy"`
		CreatedAgo string   `json:"createdAgo"`
		Run        *struct {
			Kind string `json:"kind"`
			Live bool   `json:"live"`
		} `json:"run"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.Key == "" {
		return fmt.Errorf("seed: key is required")
	}
	if _, err := w.ticket(p.Key); err == nil {
		return fmt.Errorf("seed: ticket %q already exists", p.Key)
	}
	st, err := parseState(p.State)
	if err != nil {
		return fmt.Errorf("seed %s: %w", p.Key, err)
	}
	created := w.Clock.Add(-time.Hour)
	if p.CreatedAgo != "" {
		d, err := time.ParseDuration(p.CreatedAgo)
		if err != nil {
			return fmt.Errorf("seed %s: createdAgo: %w", p.Key, err)
		}
		created = w.Clock.Add(-d)
	}
	if p.Priority == 0 {
		p.Priority = 3
	}
	t := &core.Ticket{
		ID: p.Key, Key: p.Key, State: st,
		StateSince: w.Clock, CreatedAt: created,
		Labels: p.Labels, Priority: p.Priority, Milestone: p.Milestone,
		Blocks: p.Blocks, BlockedBy: p.BlockedBy,
	}
	if p.Run != nil {
		kind, ok := agentKinds[p.Run.Kind]
		if !ok {
			return fmt.Errorf("seed %s: unknown run kind %q", p.Key, p.Run.Kind)
		}
		w.nextRun++
		t.Run = &core.Run{ID: fmt.Sprintf("run_%d", w.nextRun), Kind: kind, Live: p.Run.Live, EndedAt: w.Clock}
	}
	w.Tickets = append(w.Tickets, t)
	return nil
}

// stepTransition plays a thread that is not the control plane: an agent
// claiming, the author signing off, a scripted push-back.
func (w *World) stepTransition(raw json.RawMessage) error {
	var p struct {
		Key   string `json:"key"`
		To    string `json:"to"`
		Actor string `json:"actor"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	t, err := w.ticket(p.Key)
	if err != nil {
		return err
	}
	to, err := parseState(p.To)
	if err != nil {
		return err
	}
	actor, err := parseRole(p.Actor)
	if err != nil {
		return err
	}
	t.Last = &core.Transition{From: t.State, To: to, Actor: actor, At: w.Clock}
	t.State = to
	t.StateSince = w.Clock
	return nil
}

func (w *World) stepComment(raw json.RawMessage) error {
	var p struct {
		Key    string            `json:"key"`
		Marker string            `json:"marker"`
		Fields map[string]string `json:"fields"`
		Prose  string            `json:"prose"`
		Actor  string            `json:"actor"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	t, err := w.ticket(p.Key)
	if err != nil {
		return err
	}
	actor, err := parseRole(p.Actor)
	if err != nil {
		return err
	}
	body := p.Prose
	if p.Marker != "" {
		m := marker.Marker{Kind: marker.Kind(p.Marker), Fields: p.Fields}
		body = m.Comment(p.Prose)
	}
	t.Comments = append(t.Comments, core.Comment{Body: body, Actor: actor, At: w.Clock})
	return nil
}

func (w *World) stepLabel(raw json.RawMessage) error {
	var p struct {
		Key    string   `json:"key"`
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	t, err := w.ticket(p.Key)
	if err != nil {
		return err
	}
	for _, l := range p.Add {
		if !t.HasLabel(l) {
			t.Labels = append(t.Labels, l)
		}
	}
	for _, l := range p.Remove {
		var keep []string
		for _, have := range t.Labels {
			if have != l {
				keep = append(keep, have)
			}
		}
		t.Labels = keep
	}
	return nil
}

func (w *World) stepCI(raw json.RawMessage) error {
	var p struct {
		Key    string `json:"key"`
		Status string `json:"status"`
		Run    string `json:"run"`
		// Mergeable is the branch's merge state, empty meaning "GitHub
		// has not computed it" — which is what a scenario says when it
		// is not about conflicts at all, and is correctly inert.
		Mergeable string `json:"mergeable"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	t, err := w.ticket(p.Key)
	if err != nil {
		return err
	}
	switch core.CIStatus(p.Status) {
	case core.CIGreen, core.CIRed, core.CIPending, core.CINone:
	default:
		return fmt.Errorf("ci %s: unknown status %q", p.Key, p.Status)
	}
	switch core.MergeState(p.Mergeable) {
	case core.MergeUnknown, core.MergeClean, core.MergeConflicted:
	default:
		return fmt.Errorf("ci %s: unknown mergeable %q", p.Key, p.Mergeable)
	}
	t.CI = core.CIInfo{
		Status:    core.CIStatus(p.Status),
		RunURL:    p.Run,
		Mergeable: core.MergeState(p.Mergeable),
		PRNumber:  1,
	}
	return nil
}

func (w *World) stepDeploy(raw json.RawMessage) error {
	var p struct {
		Key    string `json:"key"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	t, err := w.ticket(p.Key)
	if err != nil {
		return err
	}
	switch core.DeployStatus(p.Status) {
	case core.DeployPending, core.DeployDeployed, core.DeployFailed, core.DeployNone:
	default:
		return fmt.Errorf("deploy %s: unknown status %q", p.Key, p.Status)
	}
	t.Deploy = core.DeployStatus(p.Status)
	return nil
}

func (w *World) stepRunEnd(raw json.RawMessage) error {
	var p struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	t, err := w.ticket(p.Key)
	if err != nil {
		return err
	}
	if t.Run == nil {
		return fmt.Errorf("run-end %s: no run", p.Key)
	}
	t.Run.Live = false
	t.Run.EndedAt = w.Clock
	return nil
}

func (w *World) stepAdvance(raw json.RawMessage) error {
	var p struct {
		By string `json:"by"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	d, err := time.ParseDuration(p.By)
	if err != nil {
		return err
	}
	w.Clock = w.Clock.Add(d)
	return nil
}

func (w *World) stepMilestone(raw json.RawMessage) error {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	w.CurrentMilestone = p.Name
	return nil
}

func (w *World) stepKill(raw json.RawMessage) error {
	var p struct {
		On bool `json:"on"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	w.KillSwitch = p.On
	return nil
}

func (w *World) stepExpect(raw json.RawMessage) error {
	var p struct {
		Key         string         `json:"key"`
		State       string         `json:"state"`
		HasLabels   []string       `json:"hasLabels"`
		LacksLabels []string       `json:"lacksLabels"`
		Assignee    string         `json:"assignee"` // "author", "nobody", or omitted
		RunKind     string         `json:"runKind"`
		RunLive     *bool          `json:"runLive"`
		Markers     map[string]int `json:"markers"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	t, err := w.ticket(p.Key)
	if err != nil {
		return err
	}
	if p.State != "" {
		want, err := parseState(p.State)
		if err != nil {
			return err
		}
		if t.State != want {
			return fmt.Errorf("expect %s: state = %q, want %q", p.Key, t.State, want)
		}
	}
	for _, l := range p.HasLabels {
		if !t.HasLabel(l) {
			return fmt.Errorf("expect %s: missing label %q (has %v)", p.Key, l, t.Labels)
		}
	}
	for _, l := range p.LacksLabels {
		if t.HasLabel(l) {
			return fmt.Errorf("expect %s: unexpected label %q", p.Key, l)
		}
	}
	switch p.Assignee {
	case "":
	case "nobody":
		if t.AssigneeID != "" {
			return fmt.Errorf("expect %s: assignee = %q, want nobody", p.Key, t.AssigneeID)
		}
	case "author":
		if t.AssigneeID != w.AuthorID() {
			return fmt.Errorf("expect %s: assignee = %q, want the author (%q)", p.Key, t.AssigneeID, w.AuthorID())
		}
	default:
		return fmt.Errorf("expect %s: assignee must be author, nobody, or omitted", p.Key)
	}
	if p.RunKind != "" {
		kind, ok := agentKinds[p.RunKind]
		if !ok {
			return fmt.Errorf("expect %s: unknown run kind %q", p.Key, p.RunKind)
		}
		if t.Run == nil || t.Run.Kind != kind {
			return fmt.Errorf("expect %s: run = %+v, want kind %q", p.Key, t.Run, kind)
		}
	}
	if p.RunLive != nil {
		live := t.Run != nil && t.Run.Live
		if live != *p.RunLive {
			return fmt.Errorf("expect %s: run live = %v, want %v (run %+v)", p.Key, live, *p.RunLive, t.Run)
		}
	}
	for kind, want := range p.Markers {
		got := 0
		for _, c := range t.Comments {
			m, ok, err := marker.Parse(c.Body)
			if err == nil && ok && m.Kind == marker.Kind(kind) {
				got++
			}
		}
		if got != want {
			return fmt.Errorf("expect %s: %d %q markers, want %d", p.Key, got, kind, want)
		}
	}
	return nil
}

func (w *World) stepExpectBoundary(raw json.RawMessage) error {
	var p struct {
		Milestone string `json:"milestone"`
		Exists    *bool  `json:"exists"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	wantExists := true
	if p.Exists != nil {
		wantExists = *p.Exists
	}
	_, err := w.ticket("B-" + p.Milestone)
	exists := err == nil
	if exists != wantExists {
		return fmt.Errorf("expect-boundary %s: exists = %v, want %v", p.Milestone, exists, wantExists)
	}
	return nil
}
