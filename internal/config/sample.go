package config

import (
	"time"

	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// Sample returns a fully-populated, valid config. It is the single fixture
// tests and the sim harness build on, and examples/pipeline.config.json
// mirrors it for humans — one canonical example, so the docs and the tests
// can't disagree about what valid looks like.
func Sample() *Config {
	return &Config{
		Version: Version,
		Tracker: Tracker{
			TeamID:    "team_sample",
			ProjectID: "project_sample",
		},
		States: map[protocol.State]string{
			protocol.Backlog:        "Backlog",
			protocol.Todo:           "Todo",
			protocol.Designing:      "Designing",
			protocol.DesignReview:   "Design review",
			protocol.ReadyForDev:    "Ready for dev",
			protocol.InProgress:     "In progress",
			protocol.Checks:         "Checks",
			protocol.Reconciling:    "Reconciling",
			protocol.ReadyForRework: "Ready for rework",
			protocol.Reworking:      "Reworking",
			protocol.Merged:         "Merged",
			protocol.BoundaryReview: "Boundary review",
			protocol.Blocked:        "Blocked",
			protocol.Done:           "Done",
			protocol.Canceled:       "Canceled",
		},
		DesignOwnedPaths: []string{
			"storybook/**",
			"screens/*.md",
			"lib/sample_web/components/**",
		},
		QualityGates: []string{
			"mix format --check-formatted",
			"mix compile --warnings-as-errors",
			"mix test",
		},
		Deploy: Deploy{
			Provider: "digitalocean",
			Endpoint: "https://api.digitalocean.com/v2/apps/sample-app-id/deployments",
			Timeout:  Duration(30 * time.Minute),
		},
		StaleClaimGrace: Duration(20 * time.Minute),
		Preview: Preview{
			PagesProject: "sample-storybook",
		},
		MilestoneNaming: "debt milestones prefixed 'Debt:', product milestones prefixed 'M:'",
		Actors: map[string][]string{
			"author":       {"usr_author"},
			"controlplane": {"memory-bot"},
		},
		Agents: map[string]string{
			"design":    "",
			"dev":       "pipeline-agent-dev.yml",
			"reconcile": "pipeline-agent-reconcile.yml",
			"boundary":  "",
		},
	}
}
