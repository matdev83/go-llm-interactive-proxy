# Hook bus scenario registry (specification bundle)

Stable identifiers for **submit hooks**, **failure modes**, **panic isolation**, and **tool reactor** chains in `internal/core/hooks`. Each row maps to `SpecBundleHookScenarios()` in [`spec_bundle_scenarios.go`](../internal/core/hooks/spec_bundle_scenarios.go).

| ID | Invariant (summary) | Primary test |
|----|---------------------|--------------|
| `SB-HOOK-submit-order` | Submit hooks run in stable order (Order, then ID, then registration index). | `TestRunSubmit_ordersByOrderField` |
| `SB-HOOK-submit-fail-open` | Fail-open submit hook errors are skipped; later hooks may run. | `TestRunSubmit_failOpen_skipsHookError` |
| `SB-HOOK-submit-fail-closed` | Fail-closed submit hook errors stop the chain. | `TestRunSubmit_failClosed_stopsChain` |
| `SB-HOOK-submit-panic-fail-open` | Fail-open: panic in one submit hook still runs subsequent hooks. | `TestRunSubmit_failOpen_panicSecondHookStillRuns` |
| `SB-HOOK-tool-reactor-chain` | Tool reactors can pass through, rewrite, or replace events in chain order. | `TestApplyToolReactors_passThroughChain` |
| `SB-HOOK-tool-reactor-invalid-canonical` | Rewrites that violate canonical tool-event shape fail closed. | `TestApplyToolReactors_rewrite_invalidCanonicalFailsClosed` |

When adding or splitting tests, update `spec_bundle_scenarios.go`, this table, and keep `TestSpecBundle_hookScenarios_referenceTests` passing (`go test -tags=precommit ./internal/core/hooks/...`).
