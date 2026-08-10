// Package host defines the port to the code host (GitHub). M0 carries only
// the dispatch surface the control plane needs to exist; PRs, runs, and
// checks arrive with M2/M3, when there is code to exercise them. The sim
// uses real temporary git repositories for repo state — git is local and
// free, so only the hosted surface (API calls) is faked.
package host

import "context"

// Dispatch is one workflow dispatch request.
type Dispatch struct {
	Workflow string
	Inputs   map[string]string
}

// Host is the port.
type Host interface {
	DispatchWorkflow(ctx context.Context, d Dispatch) error
}
