package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type budgetWorkflow struct {
	Jobs map[string]struct {
		TimeoutMinutes int `yaml:"timeout-minutes"`
	} `yaml:"jobs"`
}

func workflowJobTimeoutMinutes(t *testing.T, rel string, job string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	var workflow budgetWorkflow
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	minutes, ok := workflow.Jobs[job]
	if !ok {
		t.Fatalf("%s: job %q not found", rel, job)
	}
	return minutes.TimeoutMinutes
}

// TestMakeGateBudgets_OnlyRaceScanExceedsTheDefault locks the per-gate budget
// split: a blanket deadline under `make test-race` cannot cover the sequential
// race lanes, so race_scan declares the Release workflow's own strict-race
// budget. Every other `make` gate keeps the default, so the exception stays
// explicit instead of becoming a blanket.
func TestMakeGateBudgets_OnlyRaceScanExceedsTheDefault(t *testing.T) {
	t.Parallel()
	var race gateSpec
	seen := map[string]bool{}
	for _, g := range rootGateCatalog() {
		if g.Kind != "make" {
			continue
		}
		seen[g.Name] = true
		if g.Name == "race_scan" {
			race = g
			continue
		}
		if got := g.makeTimeout(); got != defaultMakeGateTimeout {
			t.Fatalf("gate %q budget = %s, want the default %s", g.Name, got, defaultMakeGateTimeout)
		}
	}
	if !seen["race_scan"] {
		t.Fatal("race_scan gate missing from the catalog")
	}
	if got := race.makeTimeout(); got != raceScanGateTimeout {
		t.Fatalf("race_scan budget = %s, want %s", got, raceScanGateTimeout)
	}
	if raceScanGateTimeout <= defaultMakeGateTimeout {
		t.Fatalf("race_scan budget %s must exceed the default %s", raceScanGateTimeout, defaultMakeGateTimeout)
	}
}

// TestRaceScanGateBudget_MatchesReleaseWorkflowBudget keeps the gate deadline
// exactly equal to the budget the Release workflow already grants the same
// scan, and keeps that budget inside the release-gates job that runs the gate.
// Equality, not a lower bound: the gate runs the identical script, so a larger
// deadline would let the gate outlive the workflow that owns the same evidence.
func TestRaceScanGateBudget_MatchesReleaseWorkflowBudget(t *testing.T) {
	t.Parallel()
	release := time.Duration(workflowJobTimeoutMinutes(t, ".github/workflows/release.yml", "verify")) * time.Minute
	if release <= 0 {
		t.Fatal("release verify job declares no timeout-minutes")
	}
	if raceScanGateTimeout != release {
		t.Fatalf("race_scan budget %s does not match the release workflow strict-race budget %s", raceScanGateTimeout, release)
	}
	job := time.Duration(workflowJobTimeoutMinutes(t, ".github/workflows/backend-plugin-release-gates.yml", "release-gates")) * time.Minute
	if job < raceScanGateTimeout {
		t.Fatalf("release-gates job budget %s cannot contain the race_scan budget %s", job, raceScanGateTimeout)
	}
}
