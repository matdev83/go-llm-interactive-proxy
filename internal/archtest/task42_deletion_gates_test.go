package archtest

import "testing"

// Task 4.2 production gates: after Built/Build/candidate-legacy-closer
// deletion, these are permanent zero-tolerance gates (no allowlist ever).
// Reintroducing any deleted symbol or an equivalent structural shape under a
// different name fails these tests (req 3.1-3.3, 3.8-3.10, 8.3-8.4).

func TestTask42_NoBuiltTypeDeclaration(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateTask42BuiltTypeDecl, scanTask42BuiltTypeDeclSource)
	if len(got) > 0 {
		t.Fatalf("Task 4.2: no production type named Built may exist (%d findings):\n%s", len(got), formatFindings(got))
	}
}

func TestTask42_NoCompatibilityBuildDeclaration(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateTask42BuildDecl, scanTask42BuildDeclSource)
	if len(got) > 0 {
		t.Fatalf("Task 4.2: no production runtimebundle.Build declaration may exist (%d findings):\n%s", len(got), formatFindings(got))
	}
}

func TestTask42_NoCandidateAggregateCloserField(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateTask42CandidateCloserFld, scanTask42CandidateCloserFieldSource)
	if len(got) > 0 {
		t.Fatalf("Task 4.2: no generation-runtime struct may carry an aggregate []func() error closer field (%d findings):\n%s", len(got), formatFindings(got))
	}
}

func TestTask42_NoLedgerCloserProjection(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateTask42LedgerCloserProjection, scanTask42LedgerCloserProjectionSource)
	if len(got) > 0 {
		t.Fatalf("Task 4.2: no ResourceLedger→[]func() error projection may exist (%d findings):\n%s", len(got), formatFindings(got))
	}
}

func TestTask42_NoTestOnlyConstructorInProductionFile(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanTestOnlyCtorProductionGate(t, root)
	if len(got) > 0 {
		t.Fatalf("Task 4.2: test-only ledger/candidate constructors must live in export_test.go (%d findings):\n%s", len(got), formatFindings(got))
	}
}

// scanTestOnlyCtorProductionGate reuses the production-only walk (excludes
// _test.go by construction) so a ForTest helper can only ever be found here
// if it leaked into a non-test file.
func scanTestOnlyCtorProductionGate(t *testing.T, root string) []convergenceFinding {
	t.Helper()
	return scanProductionConvergenceGate(t, root, gateTask42TestCtorInProd, scanTask42TestCtorInProductionSource)
}

// TestTask42_StdhttpBuiltAndCandidateLegacyClosersAreZero re-proves, from the
// Task 4.2 vantage point, that the Task 3.5 grandfather gates for stdhttp
// Built dependents and candidate legacy closer projections now require zero
// findings (their allowlist grandfather entries are fully retired).
func TestTask42_StdhttpBuiltAndCandidateLegacyClosersAreZero(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	built := scanProductionConvergenceGate(t, root, gateStdhttpBuilt, scanStdhttpBuiltSource)
	if len(built) > 0 {
		t.Fatalf("Task 4.2: stdhttp_built must be zero after deletion (%d findings):\n%s", len(built), formatFindings(built))
	}
	closers := scanProductionConvergenceGate(t, root, gateCandidateLegacyClosers, scanCandidateLegacyClosersSource)
	if len(closers) > 0 {
		t.Fatalf("Task 4.2: candidate_legacy_closers must be zero after deletion (%d findings):\n%s", len(closers), formatFindings(closers))
	}
}
