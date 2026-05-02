# Orchestration scenario registry (specification bundle)

Stable identifiers for **core-owned** routing and executor invariants. Each ID maps to a primary regression test in `internal/core/runtime` (see `SpecBundleOrchestrationScenarios` in `spec_bundle_scenarios.go`).

| ID | Invariant (summary) | Primary test |
|----|---------------------|--------------|
| `SB-ORCH-pre-output-recoverable` | Pre-output recoverable failures may advance route candidates and record attempt lineage. | `TestExecutor_preOutputRecoverableSwallowsAndLineage` |
| `SB-ORCH-pre-output-multi-candidate` | Multiple pre-output failures can consume candidates until one backend succeeds. | `TestExecutor_preOutputMultiOpenFailuresThenSuccess` |
| `SB-ORCH-no-failover-after-output` | After first downstream content, failures must not open a second backend or classify as recoverable pre-output for retry. | `TestExecutor_postOutputNoSecondBackendOpen` |
| `SB-ORCH-max-attempts` | `routing.max_attempts` stops further B-leg opens once exhausted. | `TestExecutor_maxAttemptsBlocksFurtherBLegs` |
| `SB-ORCH-cancel-records-attempt` | Cancellation during streaming records attempt metadata. | `TestExecutor_cancellationRecordsAttempt` |
| `SB-ORCH-weighted-first-branch` | `[first]` weighted routing persists consumed-first state on the A-leg for continuity. | `TestExecutor_weightedFirstBranch_persistsConsumed` |
| `SB-ORCH-circuit-breaker` | Routing health circuit breaker can skip unhealthy candidates. | `TestExecutor_circuitBreakerSkipsAfterFailures` |
| `SB-ORCH-backend-seam-b2bua` | Backend seam regression: pre-output recovery and no post-output failover across representative failures. | `TestExecutor_backendSeamRegression` |
| `SB-SECURE-new-session-replaces-forged` | Secure session: BeginTurn with a new session replaces a forged A-leg id. | `TestExecutor_prepareSubmitAndALeg_secure_newSession_replacesForgedALeg` |

When adding or splitting tests, update `spec_bundle_scenarios.go`, this table, and keep `TestSpecBundle_orchestrationScenarios_referenceTests` passing (`go test -tags=precommit ./internal/core/runtime/...`).
