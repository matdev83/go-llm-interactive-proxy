package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	releaseWorkflow      = ".github/workflows/release.yml"
	releaseGatesWorkflow = ".github/workflows/backend-plugin-release-gates.yml"
)

// parseWorkflow decodes a workflow document into its root mapping node.
// Workflows are read as yaml.Node rather than decoded into typed structs:
// GitHub Actions accepts `${{ ... }}` expressions anywhere a scalar is legal,
// so a single expression-valued field (for example a per-matrix-job
// timeout-minutes) must not abort the decode of the whole document and hide
// every other job from the checks below.
func parseWorkflow(t *testing.T, rel string) *yaml.Node {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("%s: workflow has no root mapping", rel)
	}
	return doc.Content[0]
}

// workflowNode walks a mapping path, returning nil as soon as a key is absent.
func workflowNode(t *testing.T, root *yaml.Node, path ...string) *yaml.Node {
	t.Helper()
	current := root
	for i, key := range path {
		if current == nil {
			return nil
		}
		if current.Kind != yaml.MappingNode {
			t.Fatalf("workflow path %v: %q is not a mapping", path[:i], path[i-1])
		}
		found := false
		for n := 0; n+1 < len(current.Content); n += 2 {
			if current.Content[n].Value == key {
				current = current.Content[n+1]
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return current
}

// workflowJobTimeout is one job's declared `timeout-minutes`. GitHub Actions
// accepts a literal minute count or a workflow expression; exactly one of
// Minutes and Expr is populated when Declared is true.
type workflowJobTimeout struct {
	Declared bool
	Minutes  int
	Expr     string
}

func (w workflowJobTimeout) String() string {
	if !w.Declared {
		return "unset"
	}
	if w.Expr != "" {
		return w.Expr
	}
	return (time.Duration(w.Minutes) * time.Minute).String()
}

func workflowJobTimeoutMinutes(t *testing.T, rel string, job string) workflowJobTimeout {
	t.Helper()
	node := workflowNode(t, parseWorkflow(t, rel), "jobs", job, "timeout-minutes")
	if node == nil {
		return workflowJobTimeout{}
	}
	if node.Kind != yaml.ScalarNode {
		t.Fatalf("%s: job %q timeout-minutes is not a scalar", rel, job)
	}
	if node.Tag != "!!int" {
		return workflowJobTimeout{Declared: true, Expr: node.Value}
	}
	var minutes int
	if err := node.Decode(&minutes); err != nil {
		t.Fatalf("%s: job %q timeout-minutes: %v", rel, job, err)
	}
	return workflowJobTimeout{Declared: true, Minutes: minutes}
}

// workflowMatrixLanes reads a job's literal `strategy.matrix.lane` list.
func workflowMatrixLanes(t *testing.T, rel string, job string) []string {
	t.Helper()
	node := workflowNode(t, parseWorkflow(t, rel), "jobs", job, "strategy", "matrix", "lane")
	if node == nil || node.Kind != yaml.SequenceNode {
		t.Fatalf("%s: job %q declares no strategy.matrix.lane sequence", rel, job)
		return nil
	}
	lanes := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		lanes = append(lanes, item.Value)
	}
	return lanes
}

// TestMakeGateBudgets_OnlyRaceScanExceedsTheDefault locks the per-gate budget
// split: a blanket deadline under `make test-race` cannot cover the sequential
// race lanes, so race_scan declares the budget that covers those lanes.
// Every other `make` gate keeps the default, so the exception stays explicit
// instead of becoming a blanket.
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

// TestRaceScanGateBudget_PartitionContractAndJobBound records exactly what the
// gate deadline does and does not assert.
//
// The Release workflow partitions the identical race script into five matrix
// lanes and picks each lane's budget from a workflow expression, so no single
// Release job budget describes the scan. This gate runs those same lanes
// sequentially in one process, and raceScanGateTimeout is only an operational
// bound on that run, not a certification that the scan completes. The contract
// is therefore deliberately narrow: the exhaustive partition must stay intact,
// and the deadline must be positive and fit inside the release-gates job that
// contains the gate. It is not compared against the verify job, and it is not
// derived from the sum of the per-lane budgets, because neither is a real
// bound on a sequential local run.
func TestRaceScanGateBudget_PartitionContractAndJobBound(t *testing.T) {
	t.Parallel()

	want := []string{"broad", "billing", "support", "runtime", "architecture"}
	lanes := workflowMatrixLanes(t, releaseWorkflow, "race")
	if len(lanes) != len(want) {
		t.Fatalf("release race matrix has lane(s) %v, want the exhaustive partition %v", lanes, want)
	}
	for i, lane := range lanes {
		if lane != want[i] {
			t.Fatalf("release race matrix lane %d = %q, want %q", i, lane, want[i])
		}
	}

	// The partition selects a budget per lane through a workflow expression; a
	// literal here would mean the five lanes were replaced by one undivided scan.
	raceBudget := workflowJobTimeoutMinutes(t, releaseWorkflow, "race")
	if !raceBudget.Declared {
		t.Fatalf("%s: job race declares no timeout-minutes", releaseWorkflow)
	}
	if raceBudget.Expr == "" {
		t.Fatalf("%s: job race budget is the literal %s; the per-lane partition is gone", releaseWorkflow, raceBudget)
	}

	if raceScanGateTimeout <= 0 {
		t.Fatalf("race_scan budget %s must be a positive deadline", raceScanGateTimeout)
	}

	job := workflowJobTimeoutMinutes(t, releaseGatesWorkflow, "release-gates")
	if !job.Declared || job.Expr != "" {
		t.Fatalf("%s: job release-gates needs a literal timeout-minutes, got %s", releaseGatesWorkflow, job)
	}
	containing := time.Duration(job.Minutes) * time.Minute
	if raceScanGateTimeout > containing {
		t.Fatalf("race_scan budget %s exceeds the release-gates job budget %s that contains it", raceScanGateTimeout, containing)
	}
}
