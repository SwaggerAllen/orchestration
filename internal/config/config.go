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
	"path/filepath"
	"slices"
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
	// State locates the pipeline's record of its own writes (DESIGN §9).
	// Absent means no store: nothing is recorded and nothing is judged,
	// which is where a project sits before it is wired up.
	State StateStore `json:"state"`

	// Disposable marks a project that exists only to rehearse the
	// pipeline, and whose tickets the scenario harness may destroy.
	// `scenario reset` requires it, so a reset aimed at a real project
	// fails on the config rather than on a prompt someone can hurry
	// past. Real project configs simply never carry it.
	Disposable bool `json:"disposable"`

	// States maps each canonical protocol state to the tracker's state name
	// for this team. All canonical states must be mapped, and no two may
	// share a tracker name: the state machine reads tracker states through
	// this table, and an ambiguous or partial table is a mis-route.
	States map[protocol.State]string `json:"states"`

	// DesignOwnedPaths are the glob patterns of DESIGN §5's ownership table.
	DesignOwnedPaths []string `json:"designOwnedPaths"`

	// NonAsksPath is the project-relative path of the confirmed non-asks
	// document (DESIGN §4) — what the design deliberately does not want,
	// each with its reason. It lives in the repo beside the screen and
	// system docs it constrains, and the design agent maintains it there
	// as part of the same artifacts commit. Defaults to
	// DefaultNonAsksPath; "" turns the feature off.
	NonAsksPath string `json:"nonAsksPath"`

	// ComponentPaths are the globs under which this project keeps its
	// component modules. The class audit (DESIGN §9) uses them to spot a
	// component arriving that the ticket never named. Optional and
	// separate from DesignOwnedPaths, which also covers docs and stories
	// — those get added every ticket, and flagging them would be noise.
	// Empty means the class audit isn't wired here; `pipeline audit`
	// says so rather than reporting a check it didn't run.
	ComponentPaths []string `json:"componentPaths"`

	// QualityGates are the blocking CI commands (DESIGN §9), run in order.
	QualityGates []string `json:"qualityGates"`

	Deploy Deploy `json:"deploy"`

	// StaleClaimGrace is how long a ticket may sit in an agent-owned state
	// with no live run before the sweep moves it to Blocked (DESIGN §12).
	StaleClaimGrace Duration `json:"staleClaimGrace"`

	Preview Preview `json:"preview"`

	// Actors maps pipeline roles to tracker user ids. The writer matrix
	// (DESIGN §3, §9) judges roles, and this table is where identity
	// becomes role — the core never sees a user id. author and
	// controlplane are required; agent roles fill in as their identities
	// exist (M3+). An id may hold only one role, or the matrix is
	// ambiguous. While agents share the control plane's API key their
	// writes resolve to controlplane, which the sweep trusts — the matrix
	// gains teeth per-agent when each agent gets its own identity.
	Actors map[string][]string `json:"actors"`

	// Root is the project repo's directory, taken from the config file's
	// own location rather than the process's working directory: the
	// agent commands run with cwd inside the pipeline checkout
	// (`go -C .pipeline run ...`), so "relative to the project" and
	// "relative to here" are two different places on every real run.
	// Never serialized — it is a fact about where the file was found,
	// not a field anyone writes.
	Root string `json:"-"`

	// Agents maps agent kinds to the project repo's stub workflow
	// filenames the control plane dispatches (DESIGN §13). An empty value
	// means the agent isn't wired yet: its dispatches are logged and
	// skipped, which is safe because the sweep re-plans them every pass.
	Agents map[string]string `json:"agents"`

	// CitationShorthands maps this project's own shorthand for a document
	// to what the citation check should do with it (DESIGN §4). Two thirds
	// of a mature project's section citations name their document by a
	// shorthand — `v5 §7.8`, `conventions §2` — which no path resolver can
	// follow, so the check sees only the explicit-path minority without
	// this table.
	//
	// Absent means no shorthands resolve, which is a smaller check rather
	// than a broken one: a project that has not written this table gets
	// exactly the explicit-path coverage it had before.
	//
	// Declared here ahead of the resolver that reads it, and that order is
	// forced rather than tidy. Load rejects unknown fields, so a project
	// config carrying this key against a binary that does not declare it
	// fails to load at all — and validation runs before everything, so the
	// whole sweep stops rather than one check degrading. That is the
	// mirror image of the protocol-state rule in CLAUDE.md: a new state
	// needs the project config first, a new config field needs this repo
	// first. Both are the same strictness read from opposite ends.
	CitationShorthands map[string]CitationShorthand `json:"citationShorthands"`
}

// CitationShorthand is one entry of Config.CitationShorthands: either a
// document this shorthand names, or a recorded reason it cannot be one.
//
// Exactly one of the two is set. The second form exists because a real
// corpus cites documents that are not in the tree at all — another
// repository's DESIGN.md, a licence text — and those citations are
// correct. Reporting them as dangling would be a false failure on a
// gate, and two of those is how a suppression gets added and the check
// goes quiet for everything after it.
//
// Behaviourally an Unchecked entry matches leaving the shorthand out of
// the table: neither resolves, neither fails. What it buys is the
// record. An absent shorthand is indistinguishable from one nobody has
// got to yet, so the next pass to notice `DESIGN §5` going unchecked
// invents a path for it and turns 15 correct citations red. The string
// is there to stop that, which makes it documentation with a reason,
// not a third code path.
type CitationShorthand struct {
	// Path is the project-relative document this shorthand names.
	Path string `json:"path"`

	// Unchecked records why this shorthand cannot resolve to a file in
	// this repository. Its content is the whole point of the entry —
	// JSON carries no comments, so a reason with nowhere to live is a
	// reason that does not get written.
	Unchecked string `json:"unchecked"`
}

// AgentKinds are the legal keys of Agents. live-suite is not an LLM
// agent — it is the project's once-per-milestone real-network test run
// (DESIGN §10) — but it dispatches through the same machinery.
var AgentKinds = []string{"design", "dev", "reconcile", "boundary", "live-suite"}

// AgentWorkflows are the workflow files agent runs come from, in
// AgentKinds order so the list is stable across loads. Unwired kinds
// contribute an empty entry, which the host skips — the caller passing
// this does not have to know which kinds a project has got to yet.
//
// It exists so the host can list agent runs per workflow rather than
// reading the repository's runs unfiltered; the reasoning is at
// github.Client.ListAgentRuns.
func (c *Config) AgentWorkflows() []string {
	out := make([]string, 0, len(AgentKinds))
	for _, k := range AgentKinds {
		out = append(out, c.Agents[k])
	}
	return out
}

// ActorRoles are the legal keys of Actors.
var ActorRoles = []string{"author", "controlplane", "design", "dev", "reconcile", "boundary"}

// sharedAuthorControlplane reports whether a duplicate id spans exactly
// the author/controlplane pair — the solo-workspace exception.
func sharedAuthorControlplane(a, b string) bool {
	return (a == "author" && b == "controlplane") || (a == "controlplane" && b == "author")
}

// DefaultNonAsksPath is where the confirmed non-asks live when a config
// names no path. Repo root, beside screens/ and systems/: the rest of
// the design is in the repo, and a record of refused decisions kept
// anywhere else is indirection with no reviewer.
//
// A project without the file is not an error — the prompt says so in as
// many words, because "nothing is recorded" and "I could not read what
// was recorded" are different facts to an agent deciding whether it is
// contradicting the author.
const DefaultNonAsksPath = "non-asks.md"

// Tracker identifies the Linear team and project. Both are required on
// every pickup: the state is the queue, the project is the scope (DESIGN §2).
type Tracker struct {
	TeamID    string `json:"teamId"`
	ProjectID string `json:"projectId"`
}

// Deploy configures post-merge deploy detection (DESIGN §13).
type Deploy struct {
	// Provider selects the adapter: "render" (Render's deploys API),
	// "digitalocean" (App Platform) or "github" (GitHub Deployments — the
	// dummy project's stand-in, which exercises the same ancestry logic;
	// PLAN M4).
	Provider string `json:"provider"`
	// Endpoint is provider-specific: the full deploys API URL for render,
	// the full deployments API URL for digitalocean, the environment name
	// (e.g. "production") for github.
	//
	// Render pages its deploy list and defaults to 20 per page, which is
	// only enough while the live deploy is within the last 20 attempts.
	// Put `?limit=` on the URL for a project that redeploys faster than
	// it merges; the adapter passes the endpoint through verbatim and
	// does not paginate.
	Endpoint string `json:"endpoint"`
	// Timeout is how long a ticket may sit in Merged before the sweep
	// moves it to Blocked (DESIGN §12).
	Timeout Duration `json:"timeout"`
}

// DeployProviders are the legal Deploy.Provider values.
var DeployProviders = []string{"render", "digitalocean", "github"}

// Preview configures the static storybook export (DESIGN §4): what builds
// it and where Cloudflare Pages serves it.
//
// The whole block is optional, and a project that omits it has no preview
// — which DESIGN §4 already calls silence rather than a dead link. It is
// being retired (PLAN §6.2, C4): design review reads the storybook from
// the running preview instead, so nothing here is built or published by
// the agent job. Comparison against the zero value is what makes "omitted"
// a state, so a field added here would quietly make an empty block
// non-empty — which is why nothing should be added to it.
type Preview struct {
	PagesProject string `json:"pagesProject"`
	// BuildCommand produces the static export; OutputDir is what gets
	// published. Both are project-specific because the export is built by
	// the project's own toolchain.
	BuildCommand string `json:"buildCommand"`
	OutputDir    string `json:"outputDir"`
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
	// An explicit "" in the file means opted out and survives; an absent
	// key means a config written before the feature existed, which
	// should get the feature rather than opt out of it.
	if !bytes.Contains(raw, []byte(`"nonAsksPath"`)) {
		c.NonAsksPath = DefaultNonAsksPath
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c.Root = filepath.Dir(abs)
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// InRoot resolves a project-relative path against the project repo. An
// absolute path is returned unchanged, and an unrooted config (Sample,
// or one built in a test) resolves against the working directory, which
// is the only sensible reading of "relative" with nothing to be
// relative to.
func (c *Config) InRoot(rel string) string {
	if rel == "" || filepath.IsAbs(rel) || c.Root == "" {
		return rel
	}
	return filepath.Join(c.Root, rel)
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
	// Both messages name DeployProviders rather than spelling the set out,
	// because the two used to be three copies of one list and a value
	// added to the var alone would have validated while the error text
	// went on denying it existed.
	switch {
	case c.Deploy.Provider == "":
		add("deploy.provider: missing (one of %s)", strings.Join(DeployProviders, ", "))
	case !slices.Contains(DeployProviders, c.Deploy.Provider):
		add("deploy.provider: %q is not a provider (one of %s)", c.Deploy.Provider, strings.Join(DeployProviders, ", "))
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
	for name, sh := range c.CitationShorthands {
		// Neither set is the zero value a typo deserializes to, and it
		// would sit in the table looking configured while resolving
		// nothing. Both set is a genuine ambiguity: the entry claims a
		// document and simultaneously claims there cannot be one.
		switch {
		case sh.Path == "" && sh.Unchecked == "":
			add("citationShorthands.%s: needs either path or unchecked", name)
		case sh.Path != "" && sh.Unchecked != "":
			add("citationShorthands.%s: has both path and unchecked, which contradict", name)
		}
	}

	// Optional as a block, required as a whole: a project either wires the
	// Cloudflare Pages export or it does not, and half of it publishes
	// nowhere while looking configured.
	//
	// Optional is the first of the three merges that retire this block
	// (PLAN §6.2, C4). Required unconditionally, it deadlocked: removing
	// the block from a project fails validation here, and removing the
	// field here makes the project's block an unknown key —
	// DisallowUnknownFields — so each side alone is invalid. That is the
	// protocol-state shape, and the ninety minutes it cost is recorded in
	// CLAUDE.md. Loosening first is what gives the project a side to go
	// first from.
	if c.Preview != (Preview{}) {
		if c.Preview.PagesProject == "" {
			add("preview.pagesProject: missing")
		}
		if c.Preview.BuildCommand == "" {
			add("preview.buildCommand: missing")
		}
		if c.Preview.OutputDir == "" {
			add("preview.outputDir: missing")
		}
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
		// One role per id — with one sanctioned exception: author and
		// controlplane may share an id, because a solo workspace's
		// personal API key IS the author. Resolution is deterministic
		// (controlplane wins, see plane.roleOf), which means the shared
		// identity is trusted everywhere and author-only enforcement is
		// off — the honest reading of a shared credential.
		seen := map[string]string{}
		for role, ids := range c.Actors {
			for _, id := range ids {
				prev, dup := seen[id]
				if dup && !sharedAuthorControlplane(prev, role) {
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

// StateStore addresses the ProjectState Durable Object in the metronome
// Worker. The token is not here — it is a secret, and it arrives in
// PIPELINE_STATE_TOKEN.
type StateStore struct {
	// URL is the Worker's origin, e.g.
	// https://pipeline-metronome.<subdomain>.workers.dev
	URL string `json:"url"`
	// Project names this project's object. Any stable string; the
	// repository name is the obvious one. Two projects sharing it would
	// share a row per ticket id, so it is worth being deliberate.
	Project string `json:"project"`
}
