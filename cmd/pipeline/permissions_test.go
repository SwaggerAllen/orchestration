package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A called workflow cannot be granted more than the calling job holds.
// A stub with no permissions block inherits the repository default,
// which is restrictive on a new repo, so a callee asking for write dies
// before a runner exists — "startup_failure", no job, no logs, nothing
// in the UI to read. It cost a live debugging round to find, and the
// only visible symptom was an agent that never ran.
func TestStubsGrantWhatTheirReusableWorkflowsAskFor(t *testing.T) {
	root := filepath.Join("..", "..")
	stubs, err := filepath.Glob(filepath.Join(root, "examples", "stubs", "*.yml"))
	if err != nil || len(stubs) == 0 {
		t.Fatalf("found no stubs to check: %v", err)
	}
	uses := regexp.MustCompile(`uses:\s*SwaggerAllen/orchestration/\.github/workflows/([\w-]+\.yml)@`)

	checked := 0
	for _, stub := range stubs {
		body, err := os.ReadFile(stub)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range uses.FindAllStringSubmatch(string(body), -1) {
			callee := m[1]
			calleeBody, err := os.ReadFile(filepath.Join(root, ".github", "workflows", callee))
			if err != nil {
				t.Errorf("%s calls %s, which is not in this repo: %v", filepath.Base(stub), callee, err)
				continue
			}
			checked++
			caller := permissionsOf(string(body))
			for scope, want := range permissionsOf(string(calleeBody)) {
				if rank(caller[scope]) < rank(want) {
					got := caller[scope]
					if got == "" {
						got = "nothing (no permissions block, or the scope is unnamed)"
					}
					t.Errorf("%s grants %s %s but %s asks for %s — the run will fail at startup with no job to inspect",
						filepath.Base(stub), scope, got, callee, want)
				}
			}
		}
	}
	if checked == 0 {
		t.Error("matched no reusable-workflow calls — the regex has drifted from the stubs")
	}
}

// permissionsOf reads a workflow's top-level permissions block. Written
// by hand because this repo carries no dependencies; the workflows are
// uniformly formatted, and a shape it cannot read returns nothing, which
// fails the comparison loudly rather than passing it quietly.
func permissionsOf(body string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "permissions:") {
			continue
		}
		if strings.Contains(line, "{}") {
			return out // explicitly nothing
		}
		for _, l := range lines[i+1:] {
			if strings.TrimSpace(l) == "" || !strings.HasPrefix(l, " ") {
				break // dedented: the block ended
			}
			k, v, found := strings.Cut(strings.TrimSpace(l), ":")
			if !found {
				continue
			}
			if c := strings.Index(v, "#"); c >= 0 {
				v = v[:c]
			}
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		return out
	}
	return out
}

func rank(level string) int {
	switch level {
	case "write":
		return 2
	case "read":
		return 1
	case "", "none":
		return 0
	}
	panic(fmt.Sprintf("unknown permission level %q", level))
}
