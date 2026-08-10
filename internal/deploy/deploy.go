// Package deploy defines the port to the deploy platform. The sweep polls
// it to run the post-deploy check and the deploy timeout (DESIGN §12, §13).
// DigitalOcean's implementation arrives in M4 alongside a fake backed by
// GitHub Deployments for the dummy project.
package deploy

import (
	"context"
	"time"
)

// Deployment is the active deployment as the platform reports it.
type Deployment struct {
	// SHA is the commit the deployment was built from. A ticket whose merge
	// SHA is an ancestor of this is deployed (DESIGN §13).
	SHA string
	// OK is whether the deployment succeeded.
	OK bool
	// FinishedAt is when the platform finished the deployment.
	FinishedAt time.Time
}

// Deploy is the port. ActiveDeployment returns nil when the platform has no
// active deployment at all.
type Deploy interface {
	ActiveDeployment(ctx context.Context) (*Deployment, error)
}
