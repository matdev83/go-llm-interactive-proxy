package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Workflows that pair path-filtered pull_request with push must not also run on
// feature-branch pushes (duplicate with the PR event). Restrict push to main.
func TestWorkflow_pushRestrictedToMainForPRDedup(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	workflows := []string{
		".github/workflows/backend-plugin-cross-platform.yml",
	}
	for _, rel := range workflows {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			var doc struct {
				On map[string]any `yaml:"on"`
			}
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("parse %s: %v", rel, err)
			}
			if doc.On == nil {
				t.Fatalf("%s missing on:", rel)
			}
			if _, ok := doc.On["pull_request"]; !ok {
				t.Fatalf("%s must keep pull_request trigger", rel)
			}
			if _, ok := doc.On["workflow_dispatch"]; !ok {
				t.Fatalf("%s must keep workflow_dispatch trigger", rel)
			}
			pushRaw, ok := doc.On["push"]
			if !ok {
				t.Fatalf("%s must keep push trigger", rel)
			}
			push, ok := pushRaw.(map[string]any)
			if !ok {
				t.Fatalf("%s push must be a mapping with branches/paths, got %T", rel, pushRaw)
			}
			branches, ok := stringSliceYAML(push["branches"])
			if !ok || len(branches) != 1 || branches[0] != "main" {
				t.Fatalf("%s push.branches must be exactly [main], got %#v", rel, push["branches"])
			}
			paths, ok := stringSliceYAML(push["paths"])
			if !ok || len(paths) == 0 {
				t.Fatalf("%s push.paths must be preserved non-empty, got %#v", rel, push["paths"])
			}
		})
	}
}

func stringSliceYAML(v any) ([]string, bool) {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	case []string:
		return t, true
	default:
		return nil, false
	}
}

// Race and release-gate lanes are nightly/weekly/manual only: a PR or push
// trigger would put per-PR minutes back on evidence that AGENTS.md assigns to
// scheduled remote CI.
func TestWorkflow_scheduledOnlyLanesHaveNoPRTrigger(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{
		".github/workflows/backend-plugin-release-gates.yml",
		".github/workflows/codex-connector-race.yml",
		".github/workflows/connector-pool-race.yml",
	} {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			var doc struct {
				On map[string]any `yaml:"on"`
			}
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("parse %s: %v", rel, err)
			}
			for _, trigger := range []string{"schedule", "workflow_dispatch"} {
				if _, ok := doc.On[trigger]; !ok {
					t.Fatalf("%s must keep %s trigger", rel, trigger)
				}
			}
			for _, trigger := range []string{"pull_request", "push"} {
				if _, ok := doc.On[trigger]; ok {
					t.Fatalf("%s must not run on %s; race and release gates are scheduled", rel, trigger)
				}
			}
		})
	}
}

// The billing race sweeps cost 35-58 minutes, about half of the nightly, so
// they run weekly: the daily cron must not select the billing lane, the weekly
// cron must select only it, and manual dispatch keeps every lane.
func TestWorkflow_raceFuzzBillingLaneIsWeekly(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "race-fuzz-nightly.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		On struct {
			Schedule []struct {
				Cron string `yaml:"cron"`
			} `yaml:"schedule"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Strategy struct {
				Matrix struct {
					Lane string `yaml:"lane"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	crons := map[string]bool{}
	for _, s := range doc.On.Schedule {
		crons[s.Cron] = true
	}
	const daily, weekly = "17 4 * * *", "47 5 * * 1"
	if len(crons) != 2 || !crons[daily] || !crons[weekly] {
		t.Fatalf("schedules = %v, want exactly daily %q and weekly %q", crons, daily, weekly)
	}
	lane := doc.Jobs["race-fuzz"].Strategy.Matrix.Lane
	for _, want := range []string{
		"github.event.schedule == '" + weekly + "' && '[\"billing\"]'",
		"github.event.schedule == '" + daily + "' && '[\"broad\",\"support\",\"runtime\",\"architecture\"]'",
		"'[\"broad\",\"billing\",\"support\",\"runtime\",\"architecture\"]'",
	} {
		if !strings.Contains(lane, want) {
			t.Fatalf("matrix lane expression missing %q:\n%s", want, lane)
		}
	}
}
