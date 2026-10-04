# BetterLeaks Secret Guard Whole-Feature Certification

## Status Report

- STATUS: BLOCKED

Date: 2026-10-04
Branch: `feat/betterleaks-secret-guard`
Commit under test: `fb7032d7220d1598b9877048879c2023e11d942d`

### Requirements and design boundary

This certification covers requirements 3.3, 3.4, 3.6, 6.7, 9.3, 9.5, 10.1, 10.2, 10.7, and 10.8, with the whole-feature boundary from task 9.1. The review covered the BetterLeaks adapter, logical-fragment scan path, hybrid merge, rewrite/quarantine bridge, feature-host composition, runtime executor boundary, diagnostics, reload composition, and the generic SDK/core import ratchets.

The fresh certification clone was created with `git clone --no-local --no-checkout` from the Windows main repository, then checked out detached at the exact feature commit. It ran as non-root `ciuser` (UID 1001) in `UbuntuOld`, with Go 1.26.6, GCC 11.4.0, and Git 2.34.1. The clone was clean before and after the gates. No production source was changed during certification.

### Gate results

| Gate | Result | Evidence |
|---|---|---|
| Focused secretguard security, architecture, and regression suite | PASS | `go test -count=1 -timeout=10m ./internal/plugins/features/secretguard/... ./internal/standardplugins/featurehost/secretguard ./internal/archtest` |
| Cross-boundary runtime, composition, ingress, QA, and SDK suite | PASS | `go test -count=1 -timeout=10m ./internal/core/runtime ./internal/core/extensions ./internal/infra/runtimebundle ./internal/stdhttp/... ./internal/qa ./pkg/lipsdk/secretguard ./pkg/lipsdk/secretguardhost` |
| Narrow reasoning-preservation race reproduction | PASS | `go test -race -count=1 -timeout=10m ./internal/plugins/features/reasoningpreservation`; the historical `pollTestPoller.Poll` race did not reproduce on the correct non-root runner |
| `make quality-checks` | PASS WITH EXPLICIT SKIPS | Native Linux quality gate exit 0; generated planes, formatting, modules, build, vet, guardrails, and architecture tests passed. `buf`, `golangci-lint`, and `staticcheck` were unavailable and the local script explicitly skipped their checks. Affected-package lint was separately verified during implementation reviews. |
| `make parity-checks` | PASS | Root contract/conformance, compatible-provider, backend-plugin, connector, and architecture parity lanes exit 0 |
| `make test-unit` | PASS | Direct native Linux retry as non-root at the exact feature commit exited 0; the complete `go test -parallel=16 -timeout=10m ./...` unit run passed, including the BetterLeaks feature, runtime, QA, and SDK packages. |
| `make qa` | FAIL | Independent direct native Linux review at the exact feature commit exited 1. `TestReplayDeepestPublishableChainTraversalIsBounded` measured stack-growth spread 131,072 B against 65,536 B slack; the gate stopped at `Makefile:523`. |
| `bash scripts/race-check.sh --strict --lane all` | PASS | Exit 0 as non-root on the native Linux clone. Billing race `2694.762s`, support-agreement race `534.496s`, runtime race `95.650s`, and architecture race `163.306s` all passed; the script reported `Race detector scan passed.` |
| Built CLI smoke | PASS | Linux `go build ./cmd/lipstd`; isolated `lipstd-linux --help` exited 0 |

The quality and parity gates include the unchanged architecture convergence ratchet, which now passes after tasks 8.4 and 8.5. The complete native Linux race gate also passed, including billing and architecture lanes. Windows race was not retried because the repository documents it as unsupported; Linux is the applicable race environment here.

### Current security evidence

The selected tests passed for zero-environment multi-user and disabled operation, invalid/absent local discovery, reload/error handling, allow-marker behavior, no-network/no-CLI/no-raw-finding guarantees, scan-failure handling, decoded redaction, quarantine/no-dispatch, deterministic merge, default-policy hash, defensive policy facts, and core/generic SDK import ratchets. Executor tests passed for block, redaction, disabled noninterference, checkpoint/backend sanitization, quarantine, and zero dispatch. No raw credential or synthetic secret value is included in this report.

The independent final unit run passed the complete repository package set, including the BetterLeaks feature packages, feature host, runtime, standard HTTP, QA, and SDK packages. The independent final QA run failed the unchanged billing schema boundedness probe. Earlier passing retries do not supersede this reproduced failure.

### Performance review assessment

Task 8.6 removed the avoidable no-hit text copies. The comparable 2 MiB text no-hit measurements are 1,221 B/op exact-only, 11,048 B/op BetterLeaks-only, and 17,816 B/op hybrid in the independently repeated final run; the tracked benchmark report records the comparable 17,906/18,210 B/op rows and the profile artifacts. The remaining positive text path materializes one 2 MiB occurrence-mapping byte representation. Hybrid reuses that representation and does not create a second request-sized copy.

The final positive 2 MiB profiles and repeated timings show approximately 311–316 ms for BetterLeaks/hybrid, with CPU dominated by the upstream regular-expression matcher and Unicode folding. The feature-owned occurrence mapping accounts for the remaining request-sized byte copy. JSON necessarily retains decode/mapping buffers: the pre-BetterLeaks 2 MiB baseline is 10.48 MB, while the post-remediation exact-only and BetterLeaks-only rows are approximately 12.58 MB and 14.70 MB. These are measured observations, not an invented numeric SLO.

Independent cross-task validation accepts the recorded bounded performance tradeoffs after the copy remediation. Further optimization can separately investigate occurrence-map admission and required JSON decode buffers while preserving immutable ownership, canonical context, clone-only redaction, cancellation, and fail-closed decoded findings. No detector default, scan limit, finding cap, confidence, decode depth, or worker setting was weakened. This satisfies the performance review trigger and does not override the failed QA gate or establish deployment latency guarantees.

### Historical initial blockers superseded by this certification

The prior initial report at commit `8c4cf3c3f0e21df067ca30f15736eb9e057c4412` recorded branch-local formatter/ineffassign/staticcheck findings, an architecture convergence delta of `-746`, Windows QA shell/worktree failures, root-user startup failures, and an incomplete full race run. Tasks 8.4, 8.5, and 8.6 plus the fresh native Linux runner supersede those facts: quality and convergence pass, the complete race matrix passes, and the old Windows/WSL worktree metadata issue is avoided by the disposable native clone. They are retained here only as historical context and are not current blockers.

### Final gate disposition

The implementer reported direct unit and QA retries with exit 0, retained only in its tool transcript. Independent review then reproduced unit exit 0 and QA exit 1 on the same clean non-root clone, with persistent complete logs. The final verdict is NO-GO. Task 9.1 remains incomplete after two bounded debug rounds; no production source or test assertion was changed to obtain passing results.

The remaining failure belongs to the unchanged billing measurement harness in `internal/core/billing/billing_schema_stress_integration_test.go:442`. The probe uses process-wide `runtime.MemStats.StackInuse`; narrow measurements varied on both main and the feature branch. Runtime allocation contamination is a supported hypothesis, not a proven production recursion defect. Route investigation to the archived `extensible-usage-economics-reconciliation` rating/certification ownership and the `billing-uncertain-component-overlap` determinism contract. Preserve the assertion and rerun comprehensive QA plus dependent BetterLeaks certification after the owning repair or adjudication.

### Artifacts and commands

Gate logs and profile outputs were kept outside the repository under `C:/Users/Mateusz/tmp/betterleaks-cert-final-fb7032d7/`. The benchmark and profile evidence is also recorded in [`benchmark-report.md`](benchmark-report.md) and the companion [`performance-assessment.md`](performance-assessment.md). The disposable Linux clone was `/home/ciuser/betterleaks-cert-fb7032d7`; it remains separate from the Windows worktree.

The prior broad-gate logs remain under `C:/Users/Mateusz/tmp/betterleaks-cert-final-fb7032d7/`; `test-unit.log` and `qa.log` there contain historical failed runs. Independent final evidence persists in UbuntuOld at `/home/ciuser/betterleaks-cert-final-evidence/review-test-unit.log` (389 lines, direct exit 0) and `review-qa.log` (425 lines, direct exit 1). Commands ran sequentially through `wsl.exe -d UbuntuOld -u ciuser -- bash -lc` with `set -o pipefail` and `tee`, so failures were not swallowed. Focused security, cross-boundary, quality with stated skips, parity, full race, and CLI evidence remains passing.

### Scope conclusion

No routing, failover, B2BUA, billing, or protocol semantics were changed outside the approved secret-guard boundary. Required QA is not green: task 9.1 is blocked and the feature is not certified for GO. Billing remediation must stay with its owning boundary.
