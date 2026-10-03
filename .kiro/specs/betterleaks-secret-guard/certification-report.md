# BetterLeaks Secret Guard Whole Feature Certification

## Status Report - STATUS: BLOCKED

Date: 2026-10-04
Branch: `feat/betterleaks-secret-guard`
Commit under test: `8c4cf3c3f0e21df067ca30f15736eb9e057c4412`

### Requirements and design boundary

This certification covers requirements 3.3, 3.4, 3.6, 6.7, 9.3, 9.5, 10.1, 10.2, 10.7, and 10.8, with the whole-feature boundary from task 9.1. The review covered the BetterLeaks adapter, logical-fragment scan path, hybrid merge, rewrite/quarantine bridge, feature-host composition, runtime executor boundary, diagnostics, reload composition, and the generic SDK/core import ratchets.

The focused architecture and security suite passed. The codegraph review showed BetterLeaks implementation ownership remains in `internal/plugins/features/secretguard`; the generic SDK and core use the approved interfaces and do not import the adapter. No production code was changed during certification. The only intended worktree change is this report.

### Gate results

| Gate | Result | Evidence |
|---|---|---|
| Focused secretguard security, architecture, and regression suite | PASS | Selected tests across `secretguard`, `engine`, feature-host, runtime, diagnostics/extensions, runtimebundle, and `internal/archtest` |
| Focused Linux race suite | PASS | WSL Ubuntu, pinned Go 1.26.6/GCC, `go test -race ... ./internal/plugins/features/secretguard/... ./internal/standardplugins/featurehost/secretguard` |
| Focused F4 durable-restart race check | PASS | `TestF4TerminalHandoffRetainsFinalLocalMeasurementAcrossDurableRestart` |
| Canonical runtime race lane | PASS | `bash scripts/race-check.sh --strict --lane runtime` |
| `make parity-checks` | PASS | Root protocol/contract parity and connector module checks |
| `make test-unit` | BLOCKED | Architecture shrinkage ratchet and Windows QA preflight failures |
| `make quality-checks` | BLOCKED | Three secretguard lint findings, one core test lint finding, and architecture shrinkage ratchet |
| `make qa` | BLOCKED | Same architecture ratchet and Windows QA preflight failures |
| Windows `make test-race` | SKIPPED BY REPOSITORY SHIM | The script documents that Go race evidence is unsupported on Windows; Linux evidence was run in WSL |
| WSL full `race-check.sh --strict --lane all` | BLOCKED/INCOMPLETE | Broad lane exposed environment/root-user and unrelated race failures; billing lane was still running when this report was captured |
| Built CLI smoke | PASS | `go build -o <isolated-temp> ./cmd/lipstd`; built artifact `lipstd.exe --help` exited 0 |

### Exact blockers and ownership

1. `internal/archtest/TestShrinkage_NetReductionMeetsRequirement115` fails the existing requirement 11.5 ratchet: raw delta `+8687`, connector overlay `2366/3550`, convergence delta `-746` with required `<= -800`; `baseline_total=19642`, `current_total=28329`. Reported contributors were `internal/infra/runtimebundle +4307`, `internal/infra/runtimehost +913`, and `internal/stdhttp +3941`, offset by `cmd/lipstd -23` and `pkg/lipruntime -451`. This needs the architecture/spec measurement owner to inspect the archived baseline and current branch contribution. The ratchet must not be weakened or silently excluded.

2. `make quality-checks` reports branch-local quality findings requiring implementation remediation: gofumpt at `internal/plugins/features/secretguard/betterleaks_adapter.go:36` and `internal/plugins/features/secretguard/race_fuzz_adversarial_test.go:21`; ineffassign at `internal/plugins/features/secretguard/scan.go:44`; and staticcheck ST1023 at `internal/core/runtime/secret_guard_observability_canary_test.go:350`. These were deliberately not changed in this certification-only task.

3. Windows QA preflight tests fail before feature assertions because the shell environment produces `awk` syntax errors, an empty/ambiguous Git revision (`fatal: Needed a single revision`), and `/bin/bash: line 3: : No such file or directory`. The WSL full lane also shows worktree-path Git failures in `internal/qa`/`tools/kiro/speccheck` (`git rev-parse HEAD` exit 128) because the Windows worktree metadata is not resolvable from the WSL path. This is an environment/QA owner issue.

4. WSL full race execution as root causes broad unrelated startup failures with `stdhttp: refusing to start as administrative user on linux`; this affects `cmd/lipstd`, `internal/stdhttp`, `internal/infra/runtimebundle`, `pkg/lipruntime`, and dependent integration tests. A separate unrelated race was also reported in `internal/plugins/features/reasoningpreservation` (`pollTestPoller.Poll` concurrent writes). These failures do not reproduce in the targeted BetterLeaks race suite or the canonical runtime lane.

5. The full WSL race script had advanced past the broad lane and was running the billing race lane when the report was captured. No final aggregate result is claimed for that still-running process. The established failures above already block a GO certification.

### Security evidence

The selected tests passed for zero-environment multi-user and disabled operation, invalid/absent local discovery, reload/error handling, allow-marker behavior, no-network/no-CLI/no-raw-finding guarantees, scan-failure handling, decoded redaction, quarantine/no-dispatch, deterministic merge, default-policy hash, defensive policy facts, and core/generic SDK import ratchets. Executor tests passed for block, redaction, disabled noninterference, checkpoint/backend sanitization, quarantine, and zero dispatch. No raw credential or synthetic secret value is included in this report.

### Performance profile assessment

Fresh profiles were written outside the repository under `C:\Users\Mateusz\tmp\betterleaks-cert-20261003\` for exact-only, BetterLeaks-only, hybrid no-hit scans, and 100 KiB positive-hit scans.

The 2 MiB no-hit benchmark measured `4,195,073 B/op` exact-only, `6,304,457 B/op` BetterLeaks-only, and `6,308,934 B/op` hybrid in the fresh run. The prior approved baseline was `2,816 B/op`; the candidate report recorded approximately `4.2 MB` exact-only and `6.3 MB` BetterLeaks/hybrid. Allocation profiles attribute about 85% of BetterLeaks-only and 75% of hybrid no-hit allocations to feature-owned conversions/copies in `walkLogicalFragments`, `betterLeaksLogicalFragmentSource.Fragments`, and `scanLogicalFragment`; CPU is dominated by the matcher/codec implementation. Positive-hit CPU is dominated by upstream `regexp` machine matching and Unicode folding, so an optimization task should first address fragment ownership and conversion boundaries rather than tune the upstream matcher.

The next bounded performance task should profile a zero-copy or single-admission fragment representation against these same cases, then obtain maintainer/design approval before changing defaults or accepting the allocation budget. No optimization, cap relaxation, or default-policy weakening was made here.

### Artifacts and commands

Tracked artifact created: `.kiro/specs/betterleaks-secret-guard/certification-report.md`.

Full gate logs and profile outputs were kept outside the repository under `C:\Users\Mateusz\tmp\betterleaks-cert-20261003\`; the WSL race log is `/tmp/betterleaks-cert-20261003/wsl-race-all.txt`. Commands included the focused security suite, `make test-unit`, `make quality-checks`, `make parity-checks`, `make qa`, Windows `make test-race`, WSL targeted and runtime race commands, WSL full `race-check.sh --strict --lane all`, profile-backed `go test -bench ... -cpuprofile/-memprofile` runs, and the built `cmd/lipstd --help` smoke test.

### Risks and required next work

Certification cannot claim GO while the existing architecture ratchet, quality findings, QA environment failures, and incomplete full race aggregate remain unresolved. The bounded next actions are to remediate the four quality findings, have the architecture owner resolve the requirement 11.5 measurement/convergence issue without weakening the gate, rerun QA/race from a Git-resolvable non-root environment, collect the final billing race aggregate, and obtain the profile-backed performance design decision.
