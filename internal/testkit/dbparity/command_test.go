package dbparity_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/dbparity"
)

func TestRunnerMode_Validation(t *testing.T) {
	for _, mode := range dbparity.ValidRunnerModes() {
		if !mode.IsValid() {
			t.Errorf("expected mode %q to be valid", mode)
		}
	}

	invalidModes := []dbparity.RunnerMode{"", "foo", "SQLITE", "postgres", "direct"}
	for _, mode := range invalidModes {
		if mode.IsValid() {
			t.Errorf("expected mode %q to be invalid", mode)
		}
	}

	parsed, err := dbparity.ParseRunnerMode("sqlite")
	if err != nil || parsed != dbparity.ModeSQLite {
		t.Fatalf("expected ModeSQLite, got %v, err: %v", parsed, err)
	}

	parsed, err = dbparity.ParseRunnerMode("  postgres-direct  ")
	if err != nil || parsed != dbparity.ModePostgresDirect {
		t.Fatalf("expected ModePostgresDirect, got %v, err: %v", parsed, err)
	}

	_, err = dbparity.ParseRunnerMode("unknown-mode")
	if err == nil {
		t.Fatal("expected error for unknown mode")
	}
	if !strings.Contains(err.Error(), "unknown runner mode") || !strings.Contains(err.Error(), "valid modes are") {
		t.Errorf("expected actionable error message listing valid modes, got: %v", err)
	}
}

func TestPlan_ListMode(t *testing.T) {
	plans, err := dbparity.Plan(dbparity.ModeList, dbparity.PlanOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plans) != 0 {
		t.Fatalf("expected 0 plans for list mode, got %d", len(plans))
	}
}

func expectedCatalogPackages(cat dbparity.Catalog) []string {
	seen := make(map[string]bool)
	var pkgs []string
	for _, comp := range cat.Components {
		for _, tp := range comp.TestPackages {
			if !seen[tp] {
				seen[tp] = true
				pkgs = append(pkgs, tp)
			}
		}
	}
	return pkgs
}

func TestPlan_SQLiteMode(t *testing.T) {
	cat := dbparity.DefaultCatalog()
	expectedPkgs := expectedCatalogPackages(cat)

	plans, err := dbparity.Plan(dbparity.ModeSQLite, dbparity.PlanOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(plans) != len(expectedPkgs) {
		t.Fatalf("expected %d plans, got %d", len(expectedPkgs), len(plans))
	}

	for i, plan := range plans {
		if plan.Backend != "sqlite" {
			t.Errorf("plan[%d] backend = %q, want 'sqlite'", i, plan.Backend)
		}
		if plan.Package != expectedPkgs[i] {
			t.Errorf("plan[%d] package = %q, want %q", i, plan.Package, expectedPkgs[i])
		}
		if plan.ComponentID == "" {
			t.Errorf("plan[%d] component ID is empty", i)
		}

		argsStr := strings.Join(plan.Args, " ")
		if !strings.HasPrefix(argsStr, "go test") {
			t.Errorf("plan[%d] args do not start with 'go test': %s", i, argsStr)
		}
		if !strings.Contains(argsStr, "-run ^TestDBParity_SQLite$") {
			t.Errorf("plan[%d] missing SQLite run selector: %s", i, argsStr)
		}
		if !strings.Contains(argsStr, "-count=1") {
			t.Errorf("plan[%d] missing -count=1 flag: %s", i, argsStr)
		}
		expectedPkgPath := "./" + expectedPkgs[i]
		if !strings.HasSuffix(argsStr, expectedPkgPath) {
			t.Errorf("plan[%d] expected package arg %q, got args: %s", i, expectedPkgPath, argsStr)
		}
	}
}

func TestPlan_PostgresDirectMode(t *testing.T) {
	cat := dbparity.DefaultCatalog()
	expectedPkgs := expectedCatalogPackages(cat)

	baseEnv := []string{
		"LIP_TEST_POSTGRES_ADMIN_DSN=postgres://admin:secret@localhost:5432/admin_db",
	}

	plans, err := dbparity.Plan(dbparity.ModePostgresDirect, dbparity.PlanOptions{
		BaseEnv: baseEnv,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(plans) != len(expectedPkgs) {
		t.Fatalf("expected %d plans, got %d", len(expectedPkgs), len(plans))
	}

	for i, plan := range plans {
		if plan.Backend != "postgres-direct" {
			t.Errorf("plan[%d] backend = %q, want 'postgres-direct'", i, plan.Backend)
		}
		if plan.Package != expectedPkgs[i] {
			t.Errorf("plan[%d] package = %q, want %q", i, plan.Package, expectedPkgs[i])
		}
		if plan.ComponentID == "" {
			t.Errorf("plan[%d] component ID is empty", i)
		}

		argsStr := strings.Join(plan.Args, " ")
		if !strings.HasPrefix(argsStr, "go test") {
			t.Errorf("plan[%d] args do not start with 'go test': %s", i, argsStr)
		}
		if !strings.Contains(argsStr, "-tags=integration") {
			t.Errorf("plan[%d] missing -tags=integration: %s", i, argsStr)
		}
		if !strings.Contains(argsStr, "-run ^TestDBParity_PostgresDirect$") {
			t.Errorf("plan[%d] missing PostgresDirect run selector: %s", i, argsStr)
		}
		if !strings.Contains(argsStr, "-skip Pooled") {
			t.Errorf("plan[%d] missing -skip Pooled: %s", i, argsStr)
		}
		if !strings.Contains(argsStr, "-count=1") {
			t.Errorf("plan[%d] missing -count=1 flag: %s", i, argsStr)
		}
		expectedPkgPath := "./" + expectedPkgs[i]
		if !strings.HasSuffix(argsStr, expectedPkgPath) {
			t.Errorf("plan[%d] expected package arg %q, got args: %s", i, expectedPkgPath, argsStr)
		}

		// Verify child environment handling
		hasRequire := false
		hasTestDSN := false
		for _, env := range plan.Env {
			if env == "LIP_REQUIRE_POSTGRES=1" {
				hasRequire = true
			}
			if strings.HasPrefix(env, "LIP_TEST_POSTGRES_DSN=") {
				hasTestDSN = true
				if env != "LIP_TEST_POSTGRES_DSN=postgres://admin:secret@localhost:5432/admin_db" {
					t.Errorf("plan[%d] expected LIP_TEST_POSTGRES_DSN fallback to admin DSN, got %q", i, env)
				}
			}
		}
		if !hasRequire {
			t.Errorf("plan[%d] missing LIP_REQUIRE_POSTGRES=1 in Env", i)
		}
		if !hasTestDSN {
			t.Errorf("plan[%d] missing LIP_TEST_POSTGRES_DSN fallback in Env", i)
		}
	}
}

func TestPlan_PostgresDirectMode_PreservesExplicitTestDSN(t *testing.T) {
	baseEnv := []string{
		"LIP_TEST_POSTGRES_DSN=postgres://runtime:runpass@localhost:5432/runtime_db",
		"LIP_TEST_POSTGRES_ADMIN_DSN=postgres://admin:adminpass@localhost:5432/admin_db",
	}

	plans, err := dbparity.Plan(dbparity.ModePostgresDirect, dbparity.PlanOptions{
		BaseEnv: baseEnv,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(plans) == 0 {
		t.Fatal("expected non-empty plans")
	}

	for _, env := range plans[0].Env {
		if strings.HasPrefix(env, "LIP_TEST_POSTGRES_DSN=") {
			t.Errorf("plan.Env should not override already-present LIP_TEST_POSTGRES_DSN, got %q", env)
		}
	}
}

func TestPlan_AllMode(t *testing.T) {
	cat := dbparity.DefaultCatalog()
	expectedPkgs := expectedCatalogPackages(cat)

	plans, err := dbparity.Plan(dbparity.ModeAll, dbparity.PlanOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(plans) != len(expectedPkgs)*2 {
		t.Fatalf("expected %d plans (sqlite + postgres-direct), got %d", len(expectedPkgs)*2, len(plans))
	}

	// First half is SQLite, second half is PostgresDirect
	for i := 0; i < len(expectedPkgs); i++ {
		if plans[i].Backend != "sqlite" {
			t.Errorf("plans[%d] backend = %q, want 'sqlite'", i, plans[i].Backend)
		}
	}
	for i := len(expectedPkgs); i < len(plans); i++ {
		if plans[i].Backend != "postgres-direct" {
			t.Errorf("plans[%d] backend = %q, want 'postgres-direct'", i, plans[i].Backend)
		}
	}
}

func TestPlan_ComponentFilter(t *testing.T) {
	plans, err := dbparity.Plan(dbparity.ModeSQLite, dbparity.PlanOptions{
		ComponentID: "billing",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan for billing, got %d", len(plans))
	}
	if plans[0].ComponentID != "billing" || plans[0].Package != "internal/infra/billingstore" {
		t.Errorf("unexpected plan: %+v", plans[0])
	}

	// secure-sessions has 2 test packages
	plans, err = dbparity.Plan(dbparity.ModeSQLite, dbparity.PlanOptions{
		ComponentID: "secure-sessions",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("expected 2 plans for secure-sessions, got %d", len(plans))
	}

	// unknown component returns actionable error
	_, err = dbparity.Plan(dbparity.ModeSQLite, dbparity.PlanOptions{
		ComponentID: "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent component")
	}
	if !strings.Contains(err.Error(), "unknown component") || !strings.Contains(err.Error(), "billing") {
		t.Errorf("expected actionable error naming valid components, got: %v", err)
	}
}

func TestPlan_GoTestFlagsPassthrough(t *testing.T) {
	flags := []string{"-timeout=5m", "-parallel=4"}
	plans, err := dbparity.Plan(dbparity.ModeSQLite, dbparity.PlanOptions{
		GoTestFlags: flags,
		ComponentID: "billing",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(plans))
	}

	argsStr := strings.Join(plans[0].Args, " ")
	if !strings.Contains(argsStr, "-timeout=5m") || !strings.Contains(argsStr, "-parallel=4") {
		t.Errorf("flags not passed through to args: %s", argsStr)
	}
}

func TestPlan_Deduplication(t *testing.T) {
	customCat := dbparity.Catalog{
		Components: []dbparity.Component{
			{
				ID:           "comp-a",
				TestPackages: []string{"pkg/shared", "pkg/a"},
			},
			{
				ID:           "comp-b",
				TestPackages: []string{"pkg/shared", "pkg/b"},
			},
		},
	}

	plans, err := dbparity.Plan(dbparity.ModeSQLite, dbparity.PlanOptions{
		Catalog: customCat,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(plans) != 3 {
		t.Fatalf("expected 3 unique package plans (shared, a, b), got %d", len(plans))
	}
	if plans[0].Package != "pkg/shared" || plans[1].Package != "pkg/a" || plans[2].Package != "pkg/b" {
		t.Errorf("unexpected deduplicated order: %+v", plans)
	}
}

func TestRedactDSN(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "standard postgres URL with user and password",
			input:    "failed connecting to postgres://user:secret123@localhost:5432/dbname",
			expected: "failed connecting to postgres://user:***@localhost:5432/dbname",
		},
		{
			name:     "postgresql URL scheme",
			input:    "connect postgresql://admin:p%40ssw0rd@db.internal:5432/test?sslmode=disable",
			expected: "connect postgresql://admin:***@db.internal:5432/test?sslmode=disable",
		},
		{
			name:     "password only without user",
			input:    "dsn is postgres://:secret@localhost:5432/db",
			expected: "dsn is postgres://:***@localhost:5432/db",
		},
		{
			name:     "URL without password",
			input:    "url postgres://user@localhost:5432/db",
			expected: "url postgres://user@localhost:5432/db",
		},
		{
			name:     "key value DSN with password",
			input:    "host=localhost port=5432 user=app password=mysecret dbname=appdb",
			expected: "host=localhost port=5432 user=app password=*** dbname=appdb",
		},
		{
			name:     "non-sensitive error message",
			input:    "dbparity: test failed for component \"billing\" package \"internal/infra/billingstore\"",
			expected: "dbparity: test failed for component \"billing\" package \"internal/infra/billingstore\"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := dbparity.RedactDSN(tc.input)
			if got != tc.expected {
				t.Errorf("RedactDSN(%q)\ngot:  %q\nwant: %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestFormatList(t *testing.T) {
	cat := dbparity.DefaultCatalog()
	textOut := dbparity.FormatList(cat)

	for _, comp := range cat.Components {
		if !strings.Contains(textOut, comp.ID) {
			t.Errorf("FormatList output missing component ID %q", comp.ID)
		}
		for _, tp := range comp.TestPackages {
			if !strings.Contains(textOut, tp) {
				t.Errorf("FormatList output missing test package %q for component %q", tp, comp.ID)
			}
		}
	}

	jsonOut, err := dbparity.FormatListJSON(cat)
	if err != nil {
		t.Fatalf("FormatListJSON failed: %v", err)
	}

	var parsed []map[string]interface{}
	if err := json.Unmarshal([]byte(jsonOut), &parsed); err != nil {
		t.Fatalf("FormatListJSON returned invalid JSON: %v", err)
	}
	if len(parsed) != len(cat.Components) {
		t.Errorf("expected %d entries in JSON list, got %d", len(cat.Components), len(parsed))
	}
}

func TestCommandPlan_Cmd(t *testing.T) {
	plan := dbparity.CommandPlan{
		ComponentID: "billing",
		Package:     "internal/infra/billingstore",
		Backend:     "postgres-direct",
		Args:        []string{"go", "test", "-tags=integration", "./internal/infra/billingstore"},
		Env:         []string{"LIP_REQUIRE_POSTGRES=1"},
	}

	baseEnv := []string{"PATH=/usr/bin"}
	cmd := plan.Cmd(context.Background(), baseEnv)
	if cmd == nil {
		t.Fatal("expected non-nil *exec.Cmd")
	}

	foundRequire := false
	foundPath := false
	for _, e := range cmd.Env {
		if e == "LIP_REQUIRE_POSTGRES=1" {
			foundRequire = true
		}
		if e == "PATH=/usr/bin" {
			foundPath = true
		}
	}
	if !foundRequire || !foundPath {
		t.Errorf("cmd.Env missing expected vars: %v", cmd.Env)
	}
}

func TestRun_ListMode(t *testing.T) {
	var stdout, stderr strings.Builder
	err := dbparity.Run(context.Background(), dbparity.ModeList, dbparity.PlanOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error running list mode: %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected empty stderr, got: %s", stderr.String())
	}
	out := stdout.String()
	for _, comp := range dbparity.DefaultCatalog().Components {
		if !strings.Contains(out, comp.ID) {
			t.Errorf("expected stdout to contain component %q", comp.ID)
		}
	}
}

func TestRun_InvalidMode(t *testing.T) {
	var stdout, stderr strings.Builder
	err := dbparity.Run(context.Background(), "invalid-mode", dbparity.PlanOptions{}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
	if !strings.Contains(err.Error(), "unknown runner mode") {
		t.Errorf("expected unknown runner mode error, got: %v", err)
	}
}

func TestRun_ComponentFilterError(t *testing.T) {
	var stdout, stderr strings.Builder
	err := dbparity.Run(context.Background(), dbparity.ModeSQLite, dbparity.PlanOptions{
		ComponentID: "invalid-comp",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for invalid component")
	}
	if !strings.Contains(err.Error(), "unknown component") {
		t.Errorf("expected unknown component error, got: %v", err)
	}
}

type stubExitError struct {
	code int
	msg  string
}

func (s *stubExitError) Error() string {
	if s.msg != "" {
		return s.msg
	}
	return fmt.Sprintf("exit status %d", s.code)
}

func (s *stubExitError) ExitCode() int {
	return s.code
}

func TestRunStepError_UnwrapAndRedaction(t *testing.T) {
	inner := &stubExitError{
		code: 42,
		msg:  "failed connecting to postgres://user:secret@localhost:5432/db: exit status 42",
	}

	stepErr := &dbparity.RunStepError{
		Component: "billing",
		Package:   "internal/infra/billingstore",
		Backend:   "postgres-direct",
		Err:       inner,
	}

	// Verify Unwrap preserves cause
	if !errors.Is(stepErr, inner) {
		t.Errorf("expected errors.Is(stepErr, inner) to be true")
	}

	var exitCoder dbparity.ExitCoder
	if !errors.As(stepErr, &exitCoder) {
		t.Fatalf("expected errors.As(stepErr, &exitCoder) to match")
	}
	if exitCoder.ExitCode() != 42 {
		t.Errorf("exitCoder.ExitCode() = %d, want 42", exitCoder.ExitCode())
	}

	// Verify Error() redacts credentials
	errStr := stepErr.Error()
	if strings.Contains(errStr, "secret") {
		t.Errorf("stepErr.Error() leaked credentials: %s", errStr)
	}
	if !strings.Contains(errStr, "postgres://user:***@localhost:5432/db") {
		t.Errorf("stepErr.Error() missing redacted DSN: %s", errStr)
	}
}

func TestMapExitStatus(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected int
	}{
		{
			name:     "nil error returns 0",
			err:      nil,
			expected: 0,
		},
		{
			name:     "direct context.Canceled returns 130",
			err:      context.Canceled,
			expected: 130,
		},
		{
			name: "wrapped context.Canceled in RunStepError returns 130",
			err: &dbparity.RunStepError{
				Component: "billing",
				Package:   "internal/infra/billingstore",
				Backend:   "sqlite",
				Err:       context.Canceled,
			},
			expected: 130,
		},
		{
			name:     "direct ExitCoder returns its code",
			err:      &stubExitError{code: 42},
			expected: 42,
		},
		{
			name: "wrapped ExitCoder in RunStepError returns its code",
			err: &dbparity.RunStepError{
				Component: "billing",
				Package:   "internal/infra/billingstore",
				Backend:   "sqlite",
				Err:       &stubExitError{code: 77},
			},
			expected: 77,
		},
		{
			name:     "generic error returns 1",
			err:      errors.New("generic execution failure"),
			expected: 1,
		},
		{
			name: "wrapped generic error in RunStepError returns 1",
			err: &dbparity.RunStepError{
				Component: "billing",
				Package:   "internal/infra/billingstore",
				Backend:   "sqlite",
				Err:       errors.New("generic execution failure"),
			},
			expected: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := dbparity.MapExitStatus(tc.err)
			if got != tc.expected {
				t.Errorf("MapExitStatus(%v) = %d, want %d", tc.err, got, tc.expected)
			}
		})
	}
}

func TestRun_ErrorPropagation_PreservesExitCode(t *testing.T) {
	var stdout, stderr strings.Builder
	stubErr := &stubExitError{code: 42}

	opts := dbparity.PlanOptions{
		ComponentID: "concurrency-authority",
		CmdRunner: func(cmd *exec.Cmd) error {
			return stubErr
		},
	}

	err := dbparity.Run(context.Background(), dbparity.ModeSQLite, opts, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected Run to return an error")
	}

	var stepErr *dbparity.RunStepError
	if !errors.As(err, &stepErr) {
		t.Fatalf("expected Run to return *RunStepError, got: %T (%v)", err, err)
	}

	if stepErr.Component != "concurrency-authority" {
		t.Errorf("stepErr.Component = %q, want 'concurrency-authority'", stepErr.Component)
	}
	if stepErr.Backend != "sqlite" {
		t.Errorf("stepErr.Backend = %q, want 'sqlite'", stepErr.Backend)
	}

	exitCode := dbparity.MapExitStatus(err)
	if exitCode != 42 {
		t.Errorf("dbparity.MapExitStatus(err) = %d, want 42", exitCode)
	}
}

func TestRun_ErrorPropagation_PreservesContextCanceled(t *testing.T) {
	var stdout, stderr strings.Builder

	opts := dbparity.PlanOptions{
		ComponentID: "concurrency-authority",
		CmdRunner: func(cmd *exec.Cmd) error {
			return context.Canceled
		},
	}

	err := dbparity.Run(context.Background(), dbparity.ModeSQLite, opts, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected Run to return an error")
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected errors.Is(err, context.Canceled) to be true, got: %v", err)
	}

	exitCode := dbparity.MapExitStatus(err)
	if exitCode != 130 {
		t.Errorf("dbparity.MapExitStatus(err) = %d, want 130", exitCode)
	}
}

func TestRun_ErrorPropagation_ContextCancellationWithExitCoder(t *testing.T) {
	// Scenario A: cancel the parent context then have CmdRunner return an *exec.ExitError-like ExitCoder;
	// assert errors.Is(err, context.Canceled) == true, MapExitStatus == 130, and RunStepError metadata is preserved.
	t.Run("canceled context with exit coder wraps context.Canceled", func(t *testing.T) {
		var stdout, stderr strings.Builder
		ctx, cancel := context.WithCancel(context.Background())

		stubErr := &stubExitError{code: 1, msg: "signal: killed"}
		opts := dbparity.PlanOptions{
			ComponentID: "concurrency-authority",
			CmdRunner: func(cmd *exec.Cmd) error {
				cancel()
				<-ctx.Done()
				return stubErr
			},
		}

		err := dbparity.Run(ctx, dbparity.ModeSQLite, opts, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected Run to return an error")
		}

		var stepErr *dbparity.RunStepError
		if !errors.As(err, &stepErr) {
			t.Fatalf("expected Run to return *RunStepError, got: %T (%v)", err, err)
		}
		if stepErr.Component != "concurrency-authority" {
			t.Errorf("stepErr.Component = %q, want 'concurrency-authority'", stepErr.Component)
		}
		if stepErr.Backend != "sqlite" {
			t.Errorf("stepErr.Backend = %q, want 'sqlite'", stepErr.Backend)
		}

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected errors.Is(err, context.Canceled) to be true, got: %v", err)
		}
		if stepErr.Err != context.Canceled {
			t.Errorf("stepErr.Err = %v, want context.Canceled", stepErr.Err)
		}

		exitCode := dbparity.MapExitStatus(err)
		if exitCode != 130 {
			t.Errorf("dbparity.MapExitStatus(err) = %d, want 130", exitCode)
		}
	})

	// Scenario B: ctx alive + failing ExitCoder -> errors.Is false and exact code preserved.
	t.Run("alive context with failing exit coder preserves exit code and is not canceled", func(t *testing.T) {
		var stdout, stderr strings.Builder
		ctx := context.Background()

		stubErr := &stubExitError{code: 42, msg: "exit status 42"}
		opts := dbparity.PlanOptions{
			ComponentID: "concurrency-authority",
			CmdRunner: func(cmd *exec.Cmd) error {
				return stubErr
			},
		}

		err := dbparity.Run(ctx, dbparity.ModeSQLite, opts, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected Run to return an error")
		}

		var stepErr *dbparity.RunStepError
		if !errors.As(err, &stepErr) {
			t.Fatalf("expected Run to return *RunStepError, got: %T (%v)", err, err)
		}
		if stepErr.Component != "concurrency-authority" {
			t.Errorf("stepErr.Component = %q, want 'concurrency-authority'", stepErr.Component)
		}
		if stepErr.Backend != "sqlite" {
			t.Errorf("stepErr.Backend = %q, want 'sqlite'", stepErr.Backend)
		}

		if errors.Is(err, context.Canceled) {
			t.Errorf("expected errors.Is(err, context.Canceled) to be false")
		}

		exitCode := dbparity.MapExitStatus(err)
		if exitCode != 42 {
			t.Errorf("dbparity.MapExitStatus(err) = %d, want 42", exitCode)
		}
	})
}



