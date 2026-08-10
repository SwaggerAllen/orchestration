// Package deploy defines the port to the deploy platform. The sweep polls
// it for the post-deploy check and the deploy timeout (DESIGN §12, §13).
// Two implementations: DigitalOcean App Platform for production, and a
// GitHub-Deployments-backed one the dummy project uses so dry runs
// exercise the real ancestry logic without a real DO app (PLAN M4).
package deploy

import "context"

// State is what the platform reports, reduced to what the post-deploy
// check needs. The `>=` comparison itself lives in the plane, which has
// the ancestry oracle (the code host).
type State struct {
	// ActiveSHA is the commit currently serving ("" if nothing is).
	ActiveSHA string
	// Failed reports that the most recent deployment attempt failed;
	// FailedSHA is the commit it was building. A ticket whose merge is in
	// a failed build is Blocked, not pending (DESIGN §11).
	Failed    bool
	FailedSHA string
}

// Deploy is the port.
type Deploy interface {
	State(ctx context.Context) (*State, error)
}
