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
// internal/infra/runtimebundle/build.go, the remaining file is the ~158-line
// Build orchestrator plus dispose/BuildExecutor helpers. The 200-line budget is
// calibrated against the reduced post-decomposition scope to lock the reduction
// and prevent the orchestrator from re-absorbing build-unit logic.
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
	{Path: "internal/infra/runtimebundle/build.go", Max: 200},
	{Path: "internal/infra/runtimebundle/options.go", Max: 200},
	{Path: "internal/standardplugins/standard_table.go", Max: 320},
	{Path: "internal/pluginreg/reg.go", Max: 320},
	{Path: "internal/stdhttp/server.go", Max: 300},
}
