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
// internal/stdhttp/server.go, the remaining file is ~206 lines. The 300-line
// budget is therefore calibrated against the reduced post-split scope, not the
// historical 608-line value, to lock the reduction and prevent re-bloat.
//
// build.go is a special case: the 591-line figure is the pre-decomposition
// historical size. After arch review Phase 2 (Tasks 2.2-2.7) extracted the
// observability/security/model/persistence/extension/executor build units out of
// internal/infra/runtimebundle/build.go, the remaining file is the ~180-line
// Build orchestrator; lifecycle validation and disposer helpers live in
// build_lifecycle.go. The 220-line budget is calibrated against this reduced
// post-decomposition scope (raised from 200 for Phase 8 concurrency authority
// wiring) to lock the reduction and prevent the orchestrator from re-absorbing
// build-unit logic.
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
	{Path: "internal/infra/runtimebundle/build.go", Max: 220},
	// Raised from 200 for issue #151 Phase 3 secret-guard compose fields on ExtensionsOptions.
	// Raised from 220 to 240 for versioned-runtime-reload task 3.2: FeatureLifecycles
	// carrier on BuildOptions (measured 227; ~13 lines headroom).
	{Path: "internal/infra/runtimebundle/options.go", Max: 240},
	{Path: "internal/standardplugins/standard_table.go", Max: 320},
	{Path: "internal/pluginreg/reg.go", Max: 320},
	{Path: "internal/stdhttp/server.go", Max: 300},

	// --- runtime-architecture-convergence-and-shrinkage Task 1.2 migration freezes ---
	// Reviewed baseline SHA efe4624909cea318c7211d5cb3734059d3210802 (Task 1.1).
	// Initial Max values are exact measured physical line counts with no growth
	// headroom. Final targets are Requirement 11.3; named tasks must lower Max.

	// Freeze 797 → final ≤300 (Req 11.3). Lower via Phase 6 task 6.5 (thin coordinator).
	{Path: "internal/infra/runtimehost/coordinator.go", Max: 797},
	// Freeze 575 → final ≤400 (Req 11.3). Lower via Phase 7 task 7.3 (generation lifecycle).
	{Path: "internal/infra/runtimehost/generation.go", Max: 575},
	// Freeze 440 → contracted to 400 in Task 3.3 (lifecycle/transfer extracted to
	// candidate_lifecycle.go); final ≤350 (Req 11.3). Task 3.5 did not further
	// contract this file (RequestPlane lived in request_plane.go); keep exact freeze.
	{Path: "internal/infra/runtimebundle/candidate_compile.go", Max: 400},
	// Task 3.5: freeze post-deletion composer/input surfaces at measured sizes.
	{Path: "internal/infra/runtimebundle/handler_composer.go", Max: 25},
	{Path: "internal/infra/runtimebundle/compile_generation.go", Max: 296},
	{Path: "internal/stdhttp/request_plane.go", Max: 52},
	{Path: "internal/stdhttp/http_input.go", Max: 99},
	// Freeze 364 → final ≤300 (Req 11.3). Lower via Phase 5 task 5.5 (process construction).
	{Path: "internal/infra/runtimebundle/process_services.go", Max: 364},
	// Freeze 367 → final ≤150 (Req 11.3). Lower via Phase 8 task 8.1 (public build/facade).
	{Path: "pkg/lipruntime/build.go", Max: 367},

	// Task 2.3: after deleting pkg/lipruntime/reload_map.go and converting public
	// mirrored types to exact SDK aliases, freeze the thin reload facade files at
	// measured post-deletion sizes (zero new headroom).
	{Path: "pkg/lipruntime/reload.go", Max: 97},
	{Path: "pkg/lipruntime/reload_aliases.go", Max: 35},
}
