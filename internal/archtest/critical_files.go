package archtest

// CriticalFileBudget caps the non-test line count for files that tend to become
// gravity wells. These are also reported as hotspots by make arch-report so the
// advisory report and the machine-checked guardrails stay in sync.
type CriticalFileBudget struct {
	Path string
	Max  int
}

// criticalFileExceedsBudget reports whether a measured physical line count
// violates a critical-file ceiling. Equality is allowed (exact freeze); Max+1
// fails so there is no silent growth headroom.
func criticalFileExceedsBudget(lines, max int) bool {
	return lines > max
}

// CriticalFileBudgets is the single source of truth for both the architecture
// guardrails test and the arch-report hotspot table. Order is preserved for
// stable report output.
//
// Historical baselines measured 2026-07-07: executor.go 416, build.go 591,
// options.go 106, standard_table.go 272, reg.go 263, server.go 608 (pre-split).
// Budgets leave ~15% headroom on the current measured values.
//
// server.go is a special case: the 608-line figure is the pre-split historical
// size. After arch review Task 1.3 split listener/lifecycle wiring out of
// internal/stdhttp/server.go, the remaining file is ~206 lines. Task 4.2
// deleted RunWithRuntime/releaseBuiltResources/runClosers entirely, leaving
// only the overridable listenAndServe var (measured 8; zero headroom).
//
// internal/infra/runtimebundle/build.go (the compatibility Build orchestrator)
// was deleted in Task 4.2; its budget entry is removed.
//
// options.go is a special case: the 106-line figure is the pre-grouping size.
// After arch review Task 2.8 grouped the flat ~30-field BuildOptions bag into
// domain sub-structs (Startup/Infra/Auth/Extensions/Policy/Diagnostics/Testing),
// the file is ~165 lines (sub-struct definitions plus moved doc comments). The
// 200-line budget accommodates the grouped form and leaves room for future
// group fields without re-bloating the flat bag (F-06).
//
// executor.go is a special case: the 416-line figure is the pre-extraction size.
// After arch review Phase 4 (Tasks 4.2-4.6) grouped executor fields, extracted
// buildRoutePlan/openInitialAttempt/assembleExecutorStream collaborators, and
// slimmed Execute to a delegate-only entrypoint (~112 lines). The 150-line budget
// locks the reduction and prevents re-bloat.
var CriticalFileBudgets = []CriticalFileBudget{
	{Path: "internal/core/runtime/executor.go", Max: 150},
	// Raised from 200 for issue #151 Phase 3 secret-guard compose fields on ExtensionsOptions.
	// Raised from 220 to 240 for versioned-runtime-reload task 3.2: FeatureLifecycles
	// carrier on BuildOptions (measured 227; ~13 lines headroom).
	{Path: "internal/infra/runtimebundle/options.go", Max: 240},
	{Path: "internal/standardplugins/standard_table.go", Max: 320},
	{Path: "internal/pluginreg/reg.go", Max: 320},
	// Ratcheted from 300 to 8 for Task 4.2: RunWithRuntime, releaseBuiltResources,
	// and runClosers deleted; only the overridable listenAndServe var remains
	// (measured 8; zero headroom).
	{Path: "internal/stdhttp/server.go", Max: 8},

	// --- runtime-architecture-convergence-and-shrinkage Task 1.2 migration freezes ---
	// Reviewed baseline SHA efe4624909cea318c7211d5cb3734059d3210802 (Task 1.1).
	// CriticalFileBudgets.Max tracks the current exact-measured ratchet (CurrentMax
	// in critical_files_freeze_test.go), not the immutable Task 1.1 BaselineMax.
	// Final targets are Requirement 11.3; named tasks must lower CurrentMax.

	// BaselineMax 797 (Task 1.1) → CurrentMax 722 after Task 6.2 AttemptGate + Abandon;
	// final ≤300 via Phase 6 task 6.5 (thin coordinator after runner/state extraction).
	{Path: "internal/infra/runtimehost/coordinator.go", Max: 722},
	// BaselineMax/CurrentMax 575 → final ≤400 (Req 11.3). Lower via Phase 7 task 7.3.
	{Path: "internal/infra/runtimehost/generation.go", Max: 575},
	// BaselineMax 440 → CurrentMax 393 after Tasks 3.3/4.2; final ≤350 (Req 11.3).
	{Path: "internal/infra/runtimebundle/candidate_compile.go", Max: 393},
	// Task 3.5: freeze post-deletion composer/input surfaces at measured sizes.
	{Path: "internal/infra/runtimebundle/handler_composer.go", Max: 25},
	{Path: "internal/infra/runtimebundle/compile_generation.go", Max: 296},
	{Path: "internal/stdhttp/request_plane.go", Max: 52},
	// Ratcheted from 99 to 42 for Task 4.2: standardHTTPInputFromBuilt deleted
	// (measured 42; zero headroom).
	{Path: "internal/stdhttp/http_input.go", Max: 42},
	// Ratcheted from 364 to 249 for Task 5.5 (final ≤300, Req 11.3): removed
	// DeferredSharedMutableOwnership/DeferredSharedMutable, ReplaceConfigForTest,
	// and DisposeProcessClosersForTest test-only compatibility surface. The
	// ProcessTracing/ProcessServices/ProcessServicesInput type declarations
	// live in process_services_types.go as a critical-file organization
	// boundary (neutral to recursive package-tree totals); this file retains
	// the construction transaction and Close/Closed methods (measured 249;
	// 51 lines under the final ≤300 target).
	{Path: "internal/infra/runtimebundle/process_services.go", Max: 249},
	// BaselineMax 367 → CurrentMax 321 (Task 5.2 thin BuildHost facade);
	// final ≤150 (Req 11.3). Lower via Phase 8 task 8.1 (public build/facade).
	{Path: "pkg/lipruntime/build.go", Max: 321},
	// Task 5.5: exact-measured post dual-bootstrap deletion (zero headroom).
	{Path: "cmd/lipstd/command.go", Max: 371},

	// Task 2.3: after deleting pkg/lipruntime/reload_map.go and converting public
	// mirrored types to exact SDK aliases, freeze the thin reload facade files at
	// measured post-deletion sizes (zero new headroom).
	{Path: "pkg/lipruntime/reload.go", Max: 97},
	{Path: "pkg/lipruntime/reload_aliases.go", Max: 35},
}
