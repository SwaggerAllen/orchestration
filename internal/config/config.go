// Package config defines the per-project configuration file (DESIGN §5):
// everything project-specific lives here, and anything not here is protocol.
// Scratch and production projects share this schema by design — the dummy
// project's config differs from a real one only in its values, which is what
// makes dry runs run the production code path.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// Version is the config schema version this binary reads. Bumped only on
// incompatible change; the validator rejects mismatches loudly rather than
// guessing, because a misread config mis-routes tickets silently.
const Version = 1

// Config is the whole per-project surface. One file, checked into the
// project repo as pipeline.config.json.
type Config struct {
	Version int     `json:"version"`
	Tracker Tracker `json:"tracker"`

	// States maps each canonical protocol state to the tracker's state name
	// for this team. All canonical states must be mapped, and no two may
	// share a tracker name: the state machine reads tracker states through
	// this table, and an ambiguous or partial table is a mis-route.
	States map[protocol.State]string `json:"states"`

	// DesignOwnedPaths are the glob patterns of DESIGN §5's ownership table.
	DesignOwnedPaths []string `json:"designOwnedPaths"`

	// QualityGates are the blocking CI commands (DESIGN §9), run in order.
	QualityGates []string `json:"qualityGates"`

	Deploy Deploy `json:"deploy"`

	// StaleClaimGrace is how long a ticket may sit in an agent-owned state
	// with no live run before the sweep moves it to Blocked (DESIGN §12).
	StaleClaimGrace Duration `json:"staleClaimGrace"`

	Preview Preview `json:"preview"`

	// MilestoneNaming is the convention identifying alternating debt and
	// product milestones (DESIGN §2.9), e.g. "debt: / product: prefixes".
	MilestoneNaming string `json:"milestoneNaming"`

	// Actors maps pipeline roles to tracker user ids. The writer matrix
	// (DESIGN §3, §9) judges roles, and this table is where identity
	// becomes role — the core never sees a user id. author and
	// controlplane are required; agent roles fill in as their identities
	// exist (M3+). An id may hold only one role, or the matrix is
	// ambiguous. While agents share the control plane's API key their
	// writes resolve to controlplane, which the sweep trusts — the matrix
	// gains teeth per-agent when each agent gets its own identity.
	Actors map[string][]string `json:"actors"`

	// Agents maps agent kinds to the project repo's stub workflow
	// filenames the control plane dispatches (DESIGN §13). An empty value
	// means the agent isn't wired yet: its dispatches are logged and
	// skipped, which is safe because the sweep re-plans them every pass.
	Agents map[string]string `json:"agents"`
}

// AgentKinds are the legal keys of Agents.
var AgentKinds = []string{"design", "dev", "reconcile", "boundary"}

// ActorRoles are the legal keys of Actors.
var ActorRoles = []string{"author", "controlplane", "design", "dev", "reconcile", "boundary"}

// Tracker identifies the Linear team and project. Both are required on
// every pickup: the state is the queue, the project is the scope (DESIGN §2).
type Tracker struct {
	TeamID    string `json:"teamId"`
	ProjectID string `json:"projectId"`
}

// Deploy configures post-merge deploy detection (DESIGN §13).
type Deploy struct {
	// Provider selects the adapter: "digitalocean" (App Platform) or
	// "github" (GitHub Deployments — the dummy project's stand-in, which
	// exercises the same ancestry logic; PLAN M4).
	Provider string `json:"provider"`
	// Endpoint is provider-specific: the full deployments API URL for
	// digitalocean; the environment name (e.g. "production") for github.
	Endpoint string `json:"endpoint"`
	// Timeout is how long a ticket may sit in Merged before the sweep
	// moves it to Blocked (DESIGN §12).
	Timeout Duration `json:"timeout"`
}

// DeployProviders are the legal Deploy.Provider values.
var DeployProviders = []string{"digitalocean", "github"}

// Preview names the Cloudflare Pages project storybook exports publish to
// (DESIGN §4).
type Preview struct {
	PagesProject string `json:"pagesProject"`
}

// Duration is a time.Duration that reads and writes Go duration strings
// ("30m", "2h") in JSON, because a bare integer's unit is a guess.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("want a duration string like \"30m\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d Duration) Duration() time.Duration { return time.Duration(d) }

// Load reads and validates a config file. Unknown fields are rejected —
// a typo that silently deserializes to a zero value is exactly the quiet
// failure this file must not have.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Validate reports every problem at once rather than the first one found:
// a config is fixed in an editor, and one round trip per field is hostile.
func (c *Config) Validate() error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if c.Version != Version {
		add("version: want %d, got %d", Version, c.Version)
	}
	if c.Tracker.TeamID == "" {
		add("tracker.teamId: missing")
	}
	if c.Tracker.ProjectID == "" {
		add("tracker.projectId: missing")
	}

	known := make(map[protocol.State]bool, len(protocol.AllStates))
	for _, s := range protocol.AllStates {
		known[s] = true
	}
	if len(c.States) == 0 {
		add("states: missing")
	} else {
		seen := map[string]protocol.State{}
		for _, s := range protocol.AllStates {
			name, ok := c.States[s]
			if !ok || name == "" {
				add("states.%s: missing", s)
				continue
			}
			if prev, dup := seen[name]; dup {
				add("states.%s: tracker name %q already used by states.%s", s, name, prev)
			}
			seen[name] = s
		}
		for s := range c.States {
			if !known[s] {
				add("states.%s: not a protocol state", s)
			}
		}
	}

	if len(c.DesignOwnedPaths) == 0 {
		add("designOwnedPaths: missing")
	}
	if len(c.QualityGates) == 0 {
		add("qualityGates: missing")
	}
	switch c.Deploy.Provider {
	case "":
		add("deploy.provider: missing (one of digitalocean, github)")
	case "digitalocean", "github":
	default:
		add("deploy.provider: %q is not a provider (one of digitalocean, github)", c.Deploy.Provider)
	}
	if c.Deploy.Endpoint == "" {
		add("deploy.endpoint: missing")
	}
	if c.Deploy.Timeout <= 0 {
		add("deploy.timeout: missing")
	}
	if c.StaleClaimGrace <= 0 {
		add("staleClaimGrace: missing")
	}
	if c.Preview.PagesProject == "" {
		add("preview.pagesProject: missing")
	}
	if c.MilestoneNaming == "" {
		add("milestoneNaming: missing")
	}

	if len(c.Actors) == 0 {
		add("actors: missing")
	} else {
		legal := map[string]bool{}
		for _, r := range ActorRoles {
			legal[r] = true
		}
		for role := range c.Actors {
			if !legal[role] {
				add("actors.%s: not a pipeline role", role)
			}
		}
		for _, required := range []string{"author", "controlplane"} {
			if len(c.Actors[required]) == 0 {
				add("actors.%s: missing — at least one tracker user id", required)
			}
		}
		seen := map[string]string{}
		for role, ids := range c.Actors {
			for _, id := range ids {
				if prev, dup := seen[id]; dup {
					add("actors.%s: id %q already assigned to %s — one role per id, or the writer matrix is ambiguous", role, id, prev)
				}
				seen[id] = role
			}
		}
	}

	if c.Agents == nil {
		add("agents: missing — map agent kinds to stub workflow filenames (empty string = not wired yet)")
	} else {
		legal := map[string]bool{}
		for _, k := range AgentKinds {
			legal[k] = true
		}
		for kind := range c.Agents {
			if !legal[kind] {
				add("agents.%s: not an agent kind", kind)
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid config:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

// StateName returns the tracker name for a canonical state. Valid configs
// map every state, so a miss is a programming error, not a user error.
func (c *Config) StateName(s protocol.State) string {
	name, ok := c.States[s]
	if !ok {
		panic(fmt.Sprintf("config: no mapping for protocol state %q", s))
	}
	return name
}
