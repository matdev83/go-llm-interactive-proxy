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
	Shell           string            `yaml:"shell"`
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

const canonicalCIDBParityFailClosedRun = "echo \"Database parity check failed or was cancelled; refusing to satisfy required repo hygiene status.\" >&2\nexit 1"

func normalizeScript(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimSpace(s)
}

func extractExecutableRunLines(run string) []string {
	lines := strings.Split(strings.ReplaceAll(run, "\r\n", "\n"), "\n")
	var execLines []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		execLines = append(execLines, trimmed)
	}
	return execLines
}

// validateCIDatabaseParityWorkflow inspects the CI workflow text against Kiro task 6.2 / requirements 7.5-7.7, 9.5
// and returns all policy violations using strict structural YAML step validation.
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
			if strings.Contains(step.If, "needs.changes.result != 'success'") && slices.Contains(extractExecutableRunLines(step.Run), "exit 1") {
				hasScopeFailClosed = true
			}
			if step.Name == "Fail closed if database parity failed" {
				if normalizeExpression(step.If) != "always() && needs.db-parity.result != 'success'" {
					violations = append(violations, "repo-hygiene fail-closed step must have exact condition 'always() && needs.db-parity.result != \\'success\\''")
				}
				if normalizeScript(step.Run) != canonicalCIDBParityFailClosedRun {
					violations = append(violations, "repo-hygiene fail-closed step run script must match canonical fail-closed script")
				} else {
					hasDBParityFailClosed = true
				}
				if step.Shell != "" && step.Shell != "bash" {
					violations = append(violations, "repo-hygiene fail-closed step shell must be empty or 'bash' (got '"+step.Shell+"')")
				}
			}
			if strings.Contains(step.If, "needs.changes.outputs.code != 'true'") && strings.Contains(step.Run, "Repo hygiene manifest check bypassed") {
				hasDocsBypass = true
			}
		}

		if !hasScopeFailClosed {
			violations = append(violations, "repo-hygiene must contain a step that fails closed when scope detection fails (needs.changes.result != 'success')")
		}
		if !hasDBParityFailClosed {
			violations = append(violations, "repo-hygiene must contain a step named 'Fail closed if database parity failed' that fails closed when database parity fails")
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
			if !strings.HasPrefix(pgService.Image, "postgres:17") || !strings.Contains(pgService.Image, "@sha256:") {
				violations = append(violations, "db-parity postgres service image must be pinned to postgres:17 by digest (@sha256:...) (got "+pgService.Image+")")
			}
		}

		var (
			hasScopeFailClosed bool
			hasEnvVerifyStep   bool
			hasParityStep      bool
			hasParityBypass    bool
		)
		for _, step := range dbParity.Steps {
			if isTruthy(step.ContinueOnError) {
				violations = append(violations, "db-parity step "+step.Name+" must not set continue-on-error: true")
			}
			if strings.Contains(step.If, "needs.changes.result != 'success'") && slices.Contains(extractExecutableRunLines(step.Run), "exit 1") {
				hasScopeFailClosed = true
			}

			if step.Name == "Verify PostgreSQL test environment" {
				if normalizeExpression(step.If) != "needs.changes.result == 'success' && needs.changes.outputs.test == 'true'" {
					violations = append(violations, "db-parity verify env step must have exact condition 'needs.changes.result == \\'success\\' && needs.changes.outputs.test == \\'true\\''")
				}
				if step.Shell != "" && step.Shell != "bash" {
					violations = append(violations, "db-parity verify env step shell must be empty or 'bash' (got '"+step.Shell+"')")
				}
				if step.Env["LIP_REQUIRE_POSTGRES"] != "1" {
					violations = append(violations, "db-parity verify env step must set LIP_REQUIRE_POSTGRES: '1'")
				}
				if step.Env["LIP_TEST_POSTGRES_DSN"] != "postgres://lip:lip@localhost:5432/lip_test?sslmode=disable" {
					violations = append(violations, "db-parity verify env step must set LIP_TEST_POSTGRES_DSN: 'postgres://lip:lip@localhost:5432/lip_test?sslmode=disable'")
				}
				if step.Env["LIP_TEST_POSTGRES_ADMIN_DSN"] != "postgres://lip:lip@localhost:5432/lip_test?sslmode=disable" {
					violations = append(violations, "db-parity verify env step must set LIP_TEST_POSTGRES_ADMIN_DSN: 'postgres://lip:lip@localhost:5432/lip_test?sslmode=disable'")
				}
				runLines := extractExecutableRunLines(step.Run)
				if !slices.Contains(runLines, "pg_isready -h localhost -p 5432 -U lip -d lip_test") {
					violations = append(violations, "db-parity verify env step must include live probe 'pg_isready -h localhost -p 5432 -U lip -d lip_test'")
				}
				for _, line := range runLines {
					if (strings.HasPrefix(line, "echo") || strings.HasPrefix(line, "printf")) && (strings.Contains(line, "$LIP_TEST_POSTGRES") || strings.Contains(line, "$LIP_MANAGED_POSTGRES") || strings.Contains(line, "${LIP_TEST_POSTGRES") || strings.Contains(line, "${LIP_MANAGED_POSTGRES") || strings.Contains(line, "postgres://")) {
						violations = append(violations, "db-parity verify env step must not interpolate DSN variables or secrets in echo/printf output")
					}
				}
				hasEnvVerifyStep = true
			}

			if strings.TrimSpace(step.Run) == "make test-db-parity" {
				hasParityStep = true
				if normalizeExpression(step.If) != "needs.changes.result == 'success' && needs.changes.outputs.test == 'true'" {
					violations = append(violations, "db-parity test step must have exact condition 'needs.changes.result == \\'success\\' && needs.changes.outputs.test == \\'true\\''")
				}
				if step.Shell != "" && step.Shell != "bash" {
					violations = append(violations, "db-parity test step shell must be empty or 'bash' (got '"+step.Shell+"')")
				}
				if step.Env["LIP_REQUIRE_POSTGRES"] != "1" {
					violations = append(violations, "db-parity test step must set LIP_REQUIRE_POSTGRES: '1'")
				}
				if step.Env["LIP_TEST_POSTGRES_DSN"] != "postgres://lip:lip@localhost:5432/lip_test?sslmode=disable" {
					violations = append(violations, "db-parity test step must set LIP_TEST_POSTGRES_DSN: 'postgres://lip:lip@localhost:5432/lip_test?sslmode=disable'")
				}
				if step.Env["LIP_TEST_POSTGRES_ADMIN_DSN"] != "postgres://lip:lip@localhost:5432/lip_test?sslmode=disable" {
					violations = append(violations, "db-parity test step must set LIP_TEST_POSTGRES_ADMIN_DSN: 'postgres://lip:lip@localhost:5432/lip_test?sslmode=disable'")
				}
				if step.Env["LIP_TEST_PARALLEL"] != "4" {
					violations = append(violations, "db-parity test step must set LIP_TEST_PARALLEL: '4'")
				}
			}

			if strings.Contains(step.If, "needs.changes.outputs.test != 'true'") && strings.Contains(step.Run, "Database parity bypassed") {
				hasParityBypass = true
			}
		}

		if !hasScopeFailClosed {
			violations = append(violations, "db-parity must contain a step that fails closed when scope detection fails")
		}
		if !hasEnvVerifyStep {
			violations = append(violations, "db-parity must contain a step named 'Verify PostgreSQL test environment' that verifies the PostgreSQL test environment and runs pg_isready")
		}
		if !hasParityStep {
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
        run: |
          echo "Database parity check failed or was cancelled; refusing to satisfy required repo hygiene status." >&2
          exit 1
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
        image: postgres:17-alpine@sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73
    steps:
      - name: Fail closed if scope detection failed
        if: "needs.changes.result != 'success'"
        run: exit 1
      - name: Verify PostgreSQL test environment
        if: "needs.changes.result == 'success' && needs.changes.outputs.test == 'true'"
        shell: bash
        env:
          LIP_REQUIRE_POSTGRES: '1'
          LIP_TEST_POSTGRES_DSN: postgres://lip:lip@localhost:5432/lip_test?sslmode=disable
          LIP_TEST_POSTGRES_ADMIN_DSN: postgres://lip:lip@localhost:5432/lip_test?sslmode=disable
        run: |
          set -euo pipefail
          if [[ -z "${LIP_TEST_POSTGRES_DSN:-}" || -z "${LIP_TEST_POSTGRES_ADMIN_DSN:-}" ]]; then
            echo "Error: required PostgreSQL test DSN is unset" >&2
            exit 1
          fi
          if [[ "${LIP_TEST_POSTGRES_DSN}" == *"POOLER"* || "${LIP_TEST_POSTGRES_ADMIN_DSN}" == *"POOLER"* ]]; then
            echo "Error: direct PostgreSQL test DSN must not target a transaction pooler" >&2
            exit 1
          fi
          if [[ "${LIP_REQUIRE_POSTGRES:-}" != "1" ]]; then
            echo "Error: LIP_REQUIRE_POSTGRES must be set to 1" >&2
            exit 1
          fi
          pg_isready -h localhost -p 5432 -U lip -d lip_test
          echo "PostgreSQL test environment verified for direct parity execution."
      - name: Run database dialect parity tests
        if: "needs.changes.result == 'success' && needs.changes.outputs.test == 'true'"
        env:
          LIP_REQUIRE_POSTGRES: '1'
          LIP_TEST_POSTGRES_DSN: postgres://lip:lip@localhost:5432/lip_test?sslmode=disable
          LIP_TEST_POSTGRES_ADMIN_DSN: postgres://lip:lip@localhost:5432/lip_test?sslmode=disable
          LIP_TEST_PARALLEL: '4'
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
		mutate     func(*testing.T, string) string
		wantSubstr string
	}{
		{
			name: "missing db-parity in repo-hygiene needs",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "needs: [changes, db-parity]", "needs: [changes]")
			},
			wantSubstr: "repo-hygiene.needs must include 'db-parity'",
		},
		{
			name: "missing step-level always() in db-parity fail-closed check",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, `if: "always() && needs.db-parity.result != 'success'"`, `if: "needs.db-parity.result != 'success'"`)
			},
			wantSubstr: "repo-hygiene fail-closed step must have exact condition",
		},
		{
			name: "neutralized db-parity fail-closed check with trailing false",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, `if: "always() && needs.db-parity.result != 'success'"`, `if: "always() && needs.db-parity.result != 'success' && false"`)
			},
			wantSubstr: "repo-hygiene fail-closed step must have exact condition",
		},
		{
			name: "weakened db-parity result check only checking failure",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, `if: "always() && needs.db-parity.result != 'success'"`, `if: "always() && needs.db-parity.result == 'failure'"`)
			},
			wantSubstr: "repo-hygiene fail-closed step must have exact condition",
		},
		{
			name: "echo-only db-parity fail-closed step without exit 1",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "echo \"Database parity check failed or was cancelled; refusing to satisfy required repo hygiene status.\" >&2\n          exit 1", "echo \"Database parity check failed or was cancelled; refusing to satisfy required repo hygiene status.\" >&2")
			},
			wantSubstr: "repo-hygiene fail-closed step run script must match canonical fail-closed script",
		},
		{
			name: "exit 0 before exit 1 in db-parity fail-closed step",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "echo \"Database parity check failed or was cancelled; refusing to satisfy required repo hygiene status.\" >&2\n          exit 1", "exit 0\n          exit 1")
			},
			wantSubstr: "repo-hygiene fail-closed step run script must match canonical fail-closed script",
		},
		{
			name: "fail-closed step renamed",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "name: Fail closed if database parity failed", "name: Optional parity check")
			},
			wantSubstr: "repo-hygiene must contain a step named 'Fail closed if database parity failed'",
		},
		{
			name: "repo-hygiene missing if: always()",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "if: always()\n    steps:", "if: success()\n    steps:")
			},
			wantSubstr: "repo-hygiene must keep 'if: always()'",
		},
		{
			name: "db-parity missing if: always()",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "name: Database parity\n    needs: changes\n    if: always()", "name: Database parity\n    needs: changes\n    if: success()")
			},
			wantSubstr: "db-parity must keep 'if: always()'",
		},
		{
			name: "repo-hygiene has continue-on-error",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "name: Repo hygiene\n    needs: [changes, db-parity]\n    if: always()", "name: Repo hygiene\n    needs: [changes, db-parity]\n    if: always()\n    continue-on-error: true")
			},
			wantSubstr: "repo-hygiene must not set continue-on-error: true",
		},
		{
			name: "db-parity has continue-on-error",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "name: Database parity\n    needs: changes\n    if: always()", "name: Database parity\n    needs: changes\n    if: always()\n    continue-on-error: true")
			},
			wantSubstr: "db-parity must not set continue-on-error: true",
		},
		{
			name: "repo-hygiene fail-closed step has continue-on-error",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "if: \"always() && needs.db-parity.result != 'success'\"", "if: \"always() && needs.db-parity.result != 'success'\"\n        continue-on-error: true")
			},
			wantSubstr: "repo-hygiene step Fail closed if database parity failed must not set continue-on-error: true",
		},
		{
			name: "db-parity missing make test-db-parity",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "run: make test-db-parity", "run: make test-unit")
			},
			wantSubstr: "db-parity must execute canonical target 'make test-db-parity'",
		},
		{
			name: "db-parity moved make test-db-parity to bypass condition",
			mutate: func(t *testing.T, s string) string {
				idx := strings.Index(s, "Run database dialect parity tests")
				return s[:idx] + mustMutate(t, s[idx:], "needs.changes.outputs.test == 'true'", "needs.changes.outputs.test != 'true'")
			},
			wantSubstr: "db-parity test step must have exact condition 'needs.changes.result == \\'success\\' && needs.changes.outputs.test == \\'true\\''",
		},
		{
			name: "db-parity test step missing LIP_REQUIRE_POSTGRES",
			mutate: func(t *testing.T, s string) string {
				idx := strings.Index(s, "Run database dialect parity tests")
				return s[:idx] + mustMutate(t, s[idx:], "LIP_REQUIRE_POSTGRES: '1'", "LIP_OTHER_VAR: '1'")
			},
			wantSubstr: "db-parity test step must set LIP_REQUIRE_POSTGRES: '1'",
		},
		{
			name: "db-parity test step missing LIP_TEST_POSTGRES_DSN",
			mutate: func(t *testing.T, s string) string {
				idx := strings.Index(s, "Run database dialect parity tests")
				return s[:idx] + mustMutate(t, s[idx:], "LIP_TEST_POSTGRES_DSN: postgres://lip:lip@localhost:5432/lip_test?sslmode=disable", "LIP_TEST_POSTGRES_DSN: postgres://wrong:5432/test")
			},
			wantSubstr: "db-parity test step must set LIP_TEST_POSTGRES_DSN",
		},
		{
			name: "db-parity test step missing LIP_TEST_POSTGRES_ADMIN_DSN",
			mutate: func(t *testing.T, s string) string {
				idx := strings.Index(s, "Run database dialect parity tests")
				return s[:idx] + mustMutate(t, s[idx:], "LIP_TEST_POSTGRES_ADMIN_DSN: postgres://lip:lip@localhost:5432/lip_test?sslmode=disable", "LIP_TEST_POSTGRES_ADMIN_DSN: postgres://wrong:5432/test")
			},
			wantSubstr: "db-parity test step must set LIP_TEST_POSTGRES_ADMIN_DSN",
		},
		{
			name: "db-parity test step missing LIP_TEST_PARALLEL",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "LIP_TEST_PARALLEL: '4'", "LIP_TEST_PARALLEL: '1'")
			},
			wantSubstr: "db-parity test step must set LIP_TEST_PARALLEL: '4'",
		},
		{
			name: "db-parity verify env step missing LIP_REQUIRE_POSTGRES",
			mutate: func(t *testing.T, s string) string {
				idx := strings.Index(s, "Verify PostgreSQL test environment")
				return s[:idx] + mustMutate(t, s[idx:], "LIP_REQUIRE_POSTGRES: '1'", "LIP_OTHER_VAR: '1'")
			},
			wantSubstr: "db-parity verify env step must set LIP_REQUIRE_POSTGRES: '1'",
		},
		{
			name: "db-parity verify env step missing LIP_TEST_POSTGRES_DSN",
			mutate: func(t *testing.T, s string) string {
				idx := strings.Index(s, "Verify PostgreSQL test environment")
				return s[:idx] + mustMutate(t, s[idx:], "LIP_TEST_POSTGRES_DSN: postgres://lip:lip@localhost:5432/lip_test?sslmode=disable", "LIP_TEST_POSTGRES_DSN: postgres://wrong:5432/test")
			},
			wantSubstr: "db-parity verify env step must set LIP_TEST_POSTGRES_DSN",
		},
		{
			name: "db-parity verify env step missing LIP_TEST_POSTGRES_ADMIN_DSN",
			mutate: func(t *testing.T, s string) string {
				idx := strings.Index(s, "Verify PostgreSQL test environment")
				return s[:idx] + mustMutate(t, s[idx:], "LIP_TEST_POSTGRES_ADMIN_DSN: postgres://lip:lip@localhost:5432/lip_test?sslmode=disable", "LIP_TEST_POSTGRES_ADMIN_DSN: postgres://wrong:5432/test")
			},
			wantSubstr: "db-parity verify env step must set LIP_TEST_POSTGRES_ADMIN_DSN",
		},
		{
			name: "db-parity postgres service image unpinned",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "image: postgres:17-alpine@sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73", "image: postgres:latest")
			},
			wantSubstr: "db-parity postgres service image must be pinned to postgres:17 by digest",
		},
		{
			name: "db-parity postgres service image missing digest",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "image: postgres:17-alpine@sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73", "image: postgres:17-alpine")
			},
			wantSubstr: "db-parity postgres service image must be pinned to postgres:17 by digest",
		},
		{
			name: "db-parity missing verify env step",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "name: Verify PostgreSQL test environment", "name: Other step")
			},
			wantSubstr: "db-parity must contain a step named 'Verify PostgreSQL test environment'",
		},
		{
			name: "db-parity verify env missing pg_isready live probe",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "pg_isready -h localhost -p 5432 -U lip -d lip_test\n          ", "")
			},
			wantSubstr: "db-parity verify env step must include live probe 'pg_isready -h localhost -p 5432 -U lip -d lip_test'",
		},
		{
			name: "db-parity verify env printing DSN via echo",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "echo \"PostgreSQL test environment verified", "echo \"DSN is $LIP_TEST_POSTGRES_DSN\"\n          echo \"PostgreSQL test environment verified")
			},
			wantSubstr: "db-parity verify env step must not interpolate DSN variables or secrets in echo/printf output",
		},
		{
			name: "db-parity verify env printing DSN via printf",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "echo \"PostgreSQL test environment verified", "printf 'DSN: %s\\n' \"$LIP_TEST_POSTGRES_DSN\"\n          echo \"PostgreSQL test environment verified")
			},
			wantSubstr: "db-parity verify env step must not interpolate DSN variables or secrets in echo/printf output",
		},
		{
			name: "custom inert shell in repo-hygiene fail-closed step",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "if: \"always() && needs.db-parity.result != 'success'\"", "if: \"always() && needs.db-parity.result != 'success'\"\n        shell: cat {0}")
			},
			wantSubstr: "repo-hygiene fail-closed step shell must be empty or 'bash'",
		},
		{
			name: "custom inert shell in db-parity test step",
			mutate: func(t *testing.T, s string) string {
				return mustMutate(t, s, "run: make test-db-parity", "shell: cat {0}\n        run: make test-db-parity")
			},
			wantSubstr: "db-parity test step shell must be empty or 'bash'",
		},
	}

	for _, tc := range negativeCases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := tc.mutate(t, validContent)
			violations := validateCIDatabaseParityWorkflow(mutated)
			joined := strings.Join(violations, "; ")
			if !strings.Contains(joined, tc.wantSubstr) {
				t.Fatalf("expected violation containing %q, got: %q", tc.wantSubstr, joined)
			}
		})
	}
}
