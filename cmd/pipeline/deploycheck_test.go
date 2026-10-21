package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/config"
	"github.com/SwaggerAllen/orchestration/internal/host"
)

// doConfig returns a config whose deploy port is DigitalOcean pointed at
// srv, which is what setup will probe.
func doConfig(endpoint string) *config.Config {
	cfg := config.Sample()
	cfg.Deploy.Provider = "digitalocean"
	cfg.Deploy.Endpoint = endpoint
	return cfg
}

// The failure this exists for: a wrong app id answers 404, and until this
// check existed the first thing to notice was a ticket stuck in Merged
// hours later, with a symptom that named neither the endpoint nor the
// token.
func TestDeployCheckFailsLoudlyOnAnEndpointItCannotRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	t.Setenv("DIGITALOCEAN_TOKEN", "do_test")

	err := checkDeploy(context.Background(), doConfig(srv.URL))
	if err == nil {
		t.Fatal("a deploy endpoint answering 404 must fail setup, not warn")
	}
	// The message has to carry what the reader needs to act: which URL was
	// tried, and what a 404 means about it.
	for _, want := range []string{srv.URL, "404", "wrong app id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestDeployCheckPassesOnAnEndpointThatAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"deployments":[{"phase":"ACTIVE","services":[{"source_commit_hash":"abc123"}]}]}`))
	}))
	defer srv.Close()
	t.Setenv("DIGITALOCEAN_TOKEN", "do_test")

	if err := checkDeploy(context.Background(), doConfig(srv.URL)); err != nil {
		t.Fatalf("a healthy endpoint must pass: %v", err)
	}
}

// A check that could not run is not a check that passed. Setup still
// succeeds — the author may be running it from a laptop that holds no
// provider token — but the output says so rather than staying silent.
func TestDeployCheckWithoutATokenSkipsRatherThanPasses(t *testing.T) {
	t.Setenv("DIGITALOCEAN_TOKEN", "")

	cfg := doConfig("https://api.digitalocean.com/v2/apps/whatever/deployments")
	if err := checkDeploy(context.Background(), cfg); err != nil {
		t.Fatalf("a missing provider token must not fail setup: %v", err)
	}
	d, why, err := deployPort(cfg, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if d != nil {
		t.Error("built a deploy port with no token")
	}
	if !strings.Contains(why, "DIGITALOCEAN_TOKEN") {
		t.Errorf("reason %q does not name the missing credential", why)
	}
}

// A project with no deploy provider configured has nothing to probe, and
// must not be told it failed one.
func TestDeployCheckIsSilentWithoutAProvider(t *testing.T) {
	cfg := config.Sample()
	cfg.Deploy.Provider = ""
	if err := checkDeploy(context.Background(), cfg); err != nil {
		t.Fatalf("no provider means nothing to check: %v", err)
	}
}

// The credential each provider reads, so the loops below can supply one
// and find out whether the provider is wired rather than whether the
// environment happens to be set.
//
// It is also the fixed list the enum is checked against. A test that only
// ranges over config.DeployProviders asserts nothing about its
// membership: deleting a provider from the enum deletes the subtest that
// covered it and the suite goes green, which is the failure mode
// CLAUDE.md records for tracker.Memory — narrowing an enumeration does not
// break a test, it removes one. So the two are compared both ways below,
// and this map is what makes adding a provider a two-file edit with a
// failing test in between rather than a silent one.
var providerCredential = map[string]func(t *testing.T){
	"digitalocean": func(t *testing.T) { t.Setenv("DIGITALOCEAN_TOKEN", "tok") },
	"render":       func(t *testing.T) { t.Setenv("RENDER_API_KEY", "tok") },
	"github":       func(t *testing.T) {}, // takes repo and token as arguments
}

func TestDeployProvidersMatchesThisTestsList(t *testing.T) {
	for _, provider := range config.DeployProviders {
		if _, ok := providerCredential[provider]; !ok {
			t.Errorf("provider %q is in config.DeployProviders but not in providerCredential — the loops below would skip it", provider)
		}
	}
	for provider := range providerCredential {
		if !slices.Contains(config.DeployProviders, provider) {
			t.Errorf("provider %q is wired and tested but not in config.DeployProviders — no project can name it", provider)
		}
	}
}

// Preflight builds a probe for every provider, and it is labelled. The
// check itself is asserted rather than the map behind it: the scope
// strings are also read out of this package's source by
// TestPreflightProbesEveryScopeTheSnapshotNeeds, and a label declared but
// never turned into a check would satisfy that grep while probing
// nothing — which is the shape of "a green preflight that means nothing"
// the grep exists to prevent.
func TestPreflightBuildsALabelledCheckForEveryProvider(t *testing.T) {
	for _, provider := range config.DeployProviders {
		t.Run(provider, func(t *testing.T) {
			providerCredential[provider](t)
			cfg := config.Sample()
			cfg.Deploy.Provider = provider
			cfg.Deploy.Endpoint = "https://example.invalid/deploys"

			dc, d, why, err := deployCheck(cfg, "owner/repo", "gh_tok")
			if err != nil {
				t.Fatalf("deployCheck: %v", err)
			}
			if d == nil {
				t.Fatalf("no deploy port for %q (reason: %q)", provider, why)
			}
			if dc.name == "" || dc.scope == "" {
				t.Errorf("check = %q/%q — preflight would print a blank column", dc.name, dc.scope)
			}
			if dc.run == nil {
				t.Error("check has no probe: preflight would report a credential it never exercised")
			}
		})
	}
}

// No port means no check, and the reason has to name the credential —
// a skipped probe printed as a clean one is the failure checkDeploy's
// own SKIPPED line exists for.
func TestPreflightSkipsWithAReasonWhenThereIsNoCredential(t *testing.T) {
	t.Setenv("RENDER_API_KEY", "")
	cfg := config.Sample()
	cfg.Deploy.Provider = "render"
	cfg.Deploy.Endpoint = "https://example.invalid/deploys"

	dc, d, why, err := deployCheck(cfg, "owner/repo", "gh_tok")
	if err != nil {
		t.Fatal(err)
	}
	if d != nil {
		t.Fatal("built a deploy port with no credential")
	}
	if dc.run != nil {
		t.Error("built a probe with no port behind it")
	}
	if !strings.Contains(why, "RENDER_API_KEY") {
		t.Errorf("reason %q does not name the missing credential", why)
	}
}

// deployPort's switch has no default, so a provider in the enum with no
// case returns a nil port and an empty reason — and nil is how "this
// project has no deploy detection" is spelled. Nothing else notices:
// config validation accepts the value, setup prints no check, the sweep
// runs, and every Merged ticket rides to the deploy timeout hours later
// with a symptom naming neither the endpoint nor the token (SETUP.md §5).
// Two lists have to stay in step for that not to happen and nothing but
// this test reads both.
func TestEveryDeployProviderHasAPort(t *testing.T) {
	for _, provider := range config.DeployProviders {
		t.Run(provider, func(t *testing.T) {
			setenv, ok := providerCredential[provider]
			if !ok {
				t.Fatalf("provider %q is in config.DeployProviders but not in this test's credential map", provider)
			}
			setenv(t)
			cfg := config.Sample()
			cfg.Deploy.Provider = provider
			cfg.Deploy.Endpoint = "https://example.invalid/deploys"

			d, why, err := deployPort(cfg, "owner/repo", "gh_tok")
			if err != nil {
				t.Fatalf("deployPort: %v", err)
			}
			if d == nil {
				t.Fatalf("no deploy port for %q (reason given: %q) — deploy detection would be silently off", provider, why)
			}
		})
	}
}

// deployPort returning a port is not the same as setup contacting it.
// This runs the check end to end, so the port is built, called, and its
// failure turned into the fatal error setup exits on — the path a wrong
// endpoint takes.
func TestSetupProbesEveryDeployProvider(t *testing.T) {
	for _, provider := range config.DeployProviders {
		t.Run(provider, func(t *testing.T) {
			providerCredential[provider](t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			}))
			defer srv.Close()

			cfg := config.Sample()
			cfg.Deploy.Provider = provider
			cfg.Deploy.Endpoint = srv.URL

			// github reads its environment from the process rather than
			// from the endpoint, and has its own base URL, so it cannot be
			// pointed at a test server; deployPort returning a port is all
			// this asserts for it.
			if provider == "github" {
				return
			}
			if err := checkDeploy(context.Background(), cfg); err == nil {
				t.Fatalf("provider %q: an endpoint answering 404 must fail setup, not go unprobed", provider)
			}
		})
	}
}

// The deploy probe has to reach the check list, not merely be built.
// Reverting the append printed `ok` against a suite that covered
// deployCheck directly — a test of the parts that says nothing about the
// wiring, which is how awaitingDispatchOf shipped.
func TestPreflightsCheckListCarriesTheDeployProbe(t *testing.T) {
	for _, provider := range config.DeployProviders {
		t.Run(provider, func(t *testing.T) {
			providerCredential[provider](t)
			cfg := config.Sample()
			cfg.Deploy.Provider = provider
			cfg.Deploy.Endpoint = "https://example.invalid/deploys"

			checks, d, why, err := hostChecks(host.NewMemory(), cfg, "owner/repo", "gh_tok")
			if err != nil {
				t.Fatalf("hostChecks: %v", err)
			}
			if d == nil {
				t.Fatalf("no deploy port for %q (reason: %q)", provider, why)
			}
			label := deployCredential[provider]
			if !slices.ContainsFunc(checks, func(c check) bool { return c.name == label[0] }) {
				var got []string
				for _, c := range checks {
					got = append(got, c.name)
				}
				t.Errorf("no deploy probe in preflight's check list for %q; list = %v", provider, got)
			}
		})
	}
}

// With no credential the list is the host checks alone — and the reason
// reaches the caller, because preflight prints it. A silently shorter
// list is a report that looks clean.
func TestPreflightsCheckListDropsTheProbeWithNoCredential(t *testing.T) {
	t.Setenv("RENDER_API_KEY", "")
	cfg := config.Sample()
	cfg.Deploy.Provider = "render"
	cfg.Deploy.Endpoint = "https://example.invalid/deploys"

	withCred := func() int {
		t.Setenv("RENDER_API_KEY", "tok")
		cs, _, _, err := hostChecks(host.NewMemory(), cfg, "owner/repo", "gh_tok")
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("RENDER_API_KEY", "")
		return len(cs)
	}()

	checks, d, why, err := hostChecks(host.NewMemory(), cfg, "owner/repo", "gh_tok")
	if err != nil {
		t.Fatal(err)
	}
	if d != nil {
		t.Fatal("built a deploy port with no credential")
	}
	if len(checks) != withCred-1 {
		t.Errorf("check list = %d, want %d (the deploy probe dropped and nothing else)", len(checks), withCred-1)
	}
	if !strings.Contains(why, "RENDER_API_KEY") {
		t.Errorf("reason %q does not name the missing credential", why)
	}
}
