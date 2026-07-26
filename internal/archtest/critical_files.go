package archtest

// CriticalFileBudget caps the non-test line count for files that tend to become
// gravity wells. These are also reported as hotspots by make arch-report so the
// advisory report and the machine-checked guardrails stay in sync.
type CriticalFileBudget struct {
	Path string
	Max  int
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
	// Raised from 220 to 245 for Phase 4 discovered-plugin install hook in Build.
	{Path: "internal/infra/runtimebundle/build.go", Max: 245},
	// Raised from 200 for issue #151 Phase 3 secret-guard compose fields on ExtensionsOptions.
	{Path: "internal/infra/runtimebundle/options.go", Max: 220},
	{Path: "internal/standardplugins/standard_table.go", Max: 320},
	// Raised from 320 for Phase 8.4 backend registration provenance (builtin vs discovered).
	{Path: "internal/pluginreg/reg.go", Max: 360},
	{Path: "internal/stdhttp/server.go", Max: 300},
}
