package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SwaggerAllen/orchestration/internal/config"
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
