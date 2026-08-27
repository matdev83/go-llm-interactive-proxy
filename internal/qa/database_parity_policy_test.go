package qa

import (
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type ciWorkflow struct {
	Name string                 `yaml:"name"`
	Jobs map[string]ciJobConfig `yaml:"jobs"`
}

type ciJobConfig struct {
	Name            string                   `yaml:"name"`
	Needs           any                      `yaml:"needs"`
	If              string                   `yaml:"if"`
	ContinueOnError any                      `yaml:"continue-on-error"`
	Services        map[string]ciServiceSpec `yaml:"services"`
	Steps           []ciStepSpec             `yaml:"steps"`
}

type ciServiceSpec struct {
	Image   string   `yaml:"image"`
	Ports   []string `yaml:"ports"`
	Options string   `yaml:"options"`
}

type ciStepSpec struct {
	Name            string            `yaml:"name"`
	If              string            `yaml:"if"`
	Run             string            `yaml:"run"`
	Uses            string            `yaml:"uses"`
	ContinueOnError any               `yaml:"continue-on-error"`
	Env             map[string]string `yaml:"env"`
}

func parseCINeeds(raw any) []string {
	switch v := raw.(type) {
	case string:
		if v != "" {
			return []string{v}
		}
		return nil
	case []any:
		var res []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				res = append(res, s)
			}
		}
		return res
	case []string:
		return v
	default:
		return nil
	}
}

func isTruthy(val any) bool {
	switch v := val.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	default:
		return false
	}
}

func normalizeExpression(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

// validateCIDatabaseParityWorkflow inspects the CI workflow text against Kiro task 6.2 / requirements 7.5-7.7, 9.5
// and returns all policy violations.
func validateCIDatabaseParityWorkflow(content string) []string {
	var violations []string

	var wf ciWorkflow
	if err := yaml.Unmarshal([]byte(content), &wf); err != nil {
		return []string{"failed to parse CI workflow YAML: " + err.Error()}
	}

	// 1. Validate repo-hygiene required aggregator
	repoHygiene, ok := wf.Jobs["repo-hygiene"]
	if !ok {
		violations = append(violations, "missing required job 'repo-hygiene'")
	} else {
		if strings.TrimSpace(repoHygiene.If) != "always()" {
			violations = append(violations, "repo-hygiene must keep 'if: always()' at job level so docs-only bypass reports success")
		}
		if isTruthy(repoHygiene.ContinueOnError) {
			violations = append(violations, "repo-hygiene must not set continue-on-error: true")
		}

		needs := parseCINeeds(repoHygiene.Needs)
		if !slices.Contains(needs, "changes") {
			violations = append(violations, "repo-hygiene.needs must include 'changes'")
		}
		if !slices.Contains(needs, "db-parity") {
			violations = append(violations, "repo-hygiene.needs must include 'db-parity' to propagate database parity result into required aggregate")
		}

		var (
			hasScopeFailClosed    bool
			hasDBParityFailClosed bool
			hasDocsBypass         bool
		)
		for _, step := range repoHygiene.Steps {
			if isTruthy(step.ContinueOnError) {
				violations = append(violations, "repo-hygiene step "+step.Name+" must not set continue-on-error: true")
			}
			if strings.Contains(step.If, "needs.changes.result != 'success'") && strings.Contains(step.Run, "exit 1") {
				hasScopeFailClosed = true
			}
			// Narrowly validate canonical fail-closed expression after whitespace normalization
			if normalizeExpression(step.If) == "always() && needs.db-parity.result != 'success'" && strings.Contains(step.Run, "exit 1") {
				hasDBParityFailClosed = true
			}
			if strings.Contains(step.If, "needs.changes.outputs.code != 'true'") && strings.Contains(step.Run, "Repo hygiene manifest check bypassed") {
				hasDocsBypass = true
			}
		}

		if !hasScopeFailClosed {
			violations = append(violations, "repo-hygiene must contain a step that fails closed when scope detection fails (needs.changes.result != 'success')")
		}
		if !hasDBParityFailClosed {
			violations = append(violations, "repo-hygiene must contain a step that fails closed when database parity fails (exact condition: 'always() && needs.db-parity.result != \\'success\\'')")
		}
		if !hasDocsBypass {
			violations = append(violations, "repo-hygiene must retain an explicit documentation-only bypass report step")
		}
	}

	// 2. Validate db-parity job
	dbParity, ok := wf.Jobs["db-parity"]
	if !ok {
		violations = append(violations, "missing job 'db-parity'")
	} else {
		if strings.TrimSpace(dbParity.If) != "always()" {
			violations = append(violations, "db-parity must keep 'if: always()' so docs-only PRs do not leave it skipped")
		}
		if isTruthy(dbParity.ContinueOnError) {
			violations = append(violations, "db-parity must not set continue-on-error: true")
		}

		needs := parseCINeeds(dbParity.Needs)
		if !slices.Contains(needs, "changes") {
			violations = append(violations, "db-parity.needs must include 'changes'")
		}

		pgService, pgOk := dbParity.Services["postgres"]
		if !pgOk {
			violations = append(violations, "db-parity must provision a postgres service container")
		} else {
			if !strings.HasPrefix(pgService.Image, "postgres:17") {
				violations = append(violations, "db-parity postgres service image must be pinned to postgres:17 (got "+pgService.Image+")")
			}
		}

		var hasScopeFailClosed, hasParityCommand, hasParityBypass bool
		for _, step := range dbParity.Steps {
			if isTruthy(step.ContinueOnError) {
				violations = append(violations, "db-parity step "+step.Name+" must not set continue-on-error: true")
			}
			if strings.Contains(step.If, "needs.changes.result != 'success'") && strings.Contains(step.Run, "exit 1") {
				hasScopeFailClosed = true
			}
			if strings.Contains(step.Run, "make test-db-parity") {
				hasParityCommand = true
				if step.Env["LIP_REQUIRE_POSTGRES"] != "1" {
					violations = append(violations, "db-parity test step must set LIP_REQUIRE_POSTGRES: '1'")
				}
			}
			if strings.Contains(step.If, "needs.changes.outputs.test != 'true'") && strings.Contains(step.Run, "Database parity bypassed") {
				hasParityBypass = true
			}
		}

		if !hasScopeFailClosed {
			violations = append(violations, "db-parity must contain a step that fails closed when scope detection fails")
		}
		if !hasParityCommand {
			violations = append(violations, "db-parity must execute canonical target 'make test-db-parity'")
		}
		if !hasParityBypass {
			violations = append(violations, "db-parity must retain an explicit documentation-only bypass report step")
		}
	}

	return violations
}

func TestDatabaseParity_CIAggregateWiring(t *testing.T) {
	t.Parallel()

	content := readRepositoryFile(t, ".github", "workflows", "ci.yml")
	violations := validateCIDatabaseParityWorkflow(content)
	if len(violations) > 0 {
		t.Fatalf("FAIL-CLOSED: CI database parity aggregate wiring violations:\n  - %s\n"+
			"Remediation: Update .github/workflows/ci.yml so repo-hygiene.needs includes db-parity "+
			"and fails closed with 'if: always() && needs.db-parity.result != \\'success\\'\\''.",
			strings.Join(violations, "\n  - "))
	}
}

func TestDatabaseParity_CIAggregateFailClosedPolicy(t *testing.T) {
	t.Parallel()

	validContent := `
name: CI
jobs:
  changes:
    name: Detect code changes
  repo-hygiene:
    name: Repo hygiene
    needs: [changes, db-parity]
    if: always()
    steps:
      - name: Fail closed if scope detection failed
        if: "needs.changes.result != 'success'"
        run: exit 1
      - name: Fail closed if database parity failed
        if: "always() && needs.db-parity.result != 'success'"
        run: exit 1
      - name: Report unrelated PR bypass
        if: "needs.changes.result == 'success' && needs.changes.outputs.code != 'true'"
        run: |
          echo "Repo hygiene manifest check bypassed: documentation-only changes detected."
  db-parity:
    name: Database parity
    needs: changes
    if: always()
    services:
      postgres:
        image: postgres:17-alpine
    steps:
      - name: Fail closed if scope detection failed
        if: "needs.changes.result != 'success'"
        run: exit 1
      - name: Run database dialect parity tests
        if: "needs.changes.result == 'success' && needs.changes.outputs.test == 'true'"
        env:
          LIP_REQUIRE_POSTGRES: '1'
        run: make test-db-parity
      - name: Report unrelated PR bypass
        if: "needs.changes.result == 'success' && needs.changes.outputs.test != 'true'"
        run: |
          echo "Database parity bypassed: documentation-only changes detected."
`

	if v := validateCIDatabaseParityWorkflow(validContent); len(v) != 0 {
		t.Fatalf("expected validContent to have 0 violations, got: %v", v)
	}

	negativeCases := []struct {
		name       string
		mutate     func(string) string
		wantSubstr string
	}{
		{
			name: "missing db-parity in repo-hygiene needs",
			mutate: func(s string) string {
				return strings.Replace(s, "needs: [changes, db-parity]", "needs: [changes]", 1)
			},
			wantSubstr: "repo-hygiene.needs must include 'db-parity'",
		},
		{
			name: "missing step-level always() in db-parity fail-closed check",
			mutate: func(s string) string {
				return strings.Replace(s, `if: "always() && needs.db-parity.result != 'success'"`, `if: "needs.db-parity.result != 'success'"`, 1)
			},
			wantSubstr: "fails closed when database parity fails",
		},
		{
			name: "neutralized db-parity fail-closed check with trailing false",
			mutate: func(s string) string {
				return strings.Replace(s, `if: "always() && needs.db-parity.result != 'success'"`, `if: "always() && needs.db-parity.result != 'success' && false"`, 1)
			},
			wantSubstr: "fails closed when database parity fails",
		},
		{
			name: "weakened db-parity result check only checking failure",
			mutate: func(s string) string {
				return strings.Replace(s, `if: "always() && needs.db-parity.result != 'success'"`, `if: "always() && needs.db-parity.result == 'failure'"`, 1)
			},
			wantSubstr: "fails closed when database parity fails",
		},
		{
			name: "ignored db-parity in fail-closed check",
			mutate: func(s string) string {
				return strings.Replace(s, `if: "always() && needs.db-parity.result != 'success'"`, `if: "always() && false"`, 1)
			},
			wantSubstr: "fails closed when database parity fails",
		},
		{
			name: "fail-closed step relocated out of repo-hygiene to db-parity",
			mutate: func(s string) string {
				// Invalidate the check in repo-hygiene
				s = strings.Replace(s, `if: "always() && needs.db-parity.result != 'success'"`, `if: "false"`, 1)
				// Relocate the valid fail-closed step to db-parity job
				return strings.Replace(s, "run: make test-db-parity", "run: make test-db-parity\n      - name: Fail closed if database parity failed\n        if: \"always() && needs.db-parity.result != 'success'\"\n        run: exit 1", 1)
			},
			wantSubstr: "fails closed when database parity fails",
		},
		{
			name: "repo-hygiene missing if: always()",
			mutate: func(s string) string {
				return strings.Replace(s, "if: always()\n    steps:", "if: success()\n    steps:", 1)
			},
			wantSubstr: "repo-hygiene must keep 'if: always()'",
		},
		{
			name: "db-parity missing if: always()",
			mutate: func(s string) string {
				return strings.Replace(s, "name: Database parity\n    needs: changes\n    if: always()", "name: Database parity\n    needs: changes\n    if: success()", 1)
			},
			wantSubstr: "db-parity must keep 'if: always()'",
		},
		{
			name: "repo-hygiene has continue-on-error",
			mutate: func(s string) string {
				return strings.Replace(s, "name: Repo hygiene\n    needs: [changes, db-parity]\n    if: always()", "name: Repo hygiene\n    needs: [changes, db-parity]\n    if: always()\n    continue-on-error: true", 1)
			},
			wantSubstr: "repo-hygiene must not set continue-on-error: true",
		},
		{
			name: "db-parity has continue-on-error",
			mutate: func(s string) string {
				return strings.Replace(s, "name: Database parity\n    needs: changes\n    if: always()", "name: Database parity\n    needs: changes\n    if: always()\n    continue-on-error: true", 1)
			},
			wantSubstr: "db-parity must not set continue-on-error: true",
		},
		{
			name: "repo-hygiene fail-closed step has continue-on-error",
			mutate: func(s string) string {
				return strings.Replace(s, "if: \"always() && needs.db-parity.result != 'success'\"\n        run: exit 1", "if: \"always() && needs.db-parity.result != 'success'\"\n        continue-on-error: true\n        run: exit 1", 1)
			},
			wantSubstr: "repo-hygiene step Fail closed if database parity failed must not set continue-on-error: true",
		},
		{
			name: "db-parity missing make test-db-parity",
			mutate: func(s string) string {
				return strings.Replace(s, "run: make test-db-parity", "run: make test-unit", 1)
			},
			wantSubstr: "must execute canonical target 'make test-db-parity'",
		},
	}

	for _, tc := range negativeCases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := tc.mutate(validContent)
			violations := validateCIDatabaseParityWorkflow(mutated)
			joined := strings.Join(violations, "; ")
			if !strings.Contains(joined, tc.wantSubstr) {
				t.Fatalf("expected violation containing %q, got: %q", tc.wantSubstr, joined)
			}
		})
	}
}

