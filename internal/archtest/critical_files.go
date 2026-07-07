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
var CriticalFileBudgets = []CriticalFileBudget{
	{Path: "internal/core/runtime/executor.go", Max: 480},
	{Path: "internal/infra/runtimebundle/build.go", Max: 680},
	{Path: "internal/infra/runtimebundle/options.go", Max: 130},
	{Path: "internal/pluginreg/standard_table.go", Max: 320},
	{Path: "internal/pluginreg/reg.go", Max: 320},
	{Path: "internal/stdhttp/server.go", Max: 300},
}
