# Implementation Ledger

This ledger records reviewed implementation slices. `tasks.md` has no task
checkboxes, so task completion is recorded here without altering its plan text.

## Approval and prerequisite

| Commit | Scope | Evidence |
| --- | --- | --- |
| `8c1e7e67` | Requirements, design, and tasks accepted; spec ready for implementation | `origin/main` at `2b30116c` contains the merged `compaction-event-detection` detector runtime; branch rebase reported up to date; `spec.json` strict JSON parse and diff check passed |

## Wave 1 — Preservation foundations

Commit: `fd47a418` (`feat(compaction): add continuity preservation foundations`)

| Task | Status | Reviewed implementation | Verification |
| --- | --- | --- | --- |
| 1.1 | Partial | Pure detector request/response preview, shared recognition authority, stable completion-only boundary fingerprint, near-miss fixtures | `go test -count=1 ./internal/core/compactiondetect ./internal/core/runtime` |
| 1.2 | Partial | Separate preserver SDK contract, ordered bundle/snapshot composition, transactional fail-open request/event dispatch | `go test -count=1 ./pkg/lipsdk/... ./internal/featurebundle/... ./internal/core/extensions/...` |
| 1.3 | Complete, race runner unavailable | Bounded keyed scheduler, submit-time runner and `KindAsync` pin capture, independent worker context, retention, Await/Forget, shutdown, panic and pin cleanup, goleak coverage | `go test -count=20 ./internal/core/auxreq`; checkptr clone stress `-count=50`; targeted `go test -race` blocked before tests by Windows ThreadSanitizer allocation error 87 |
| 2.1 | Complete | Detector previews delegate to the same detector-owned matching and fingerprint helpers as committed lifecycle methods | Focused detector/runtime suites green |
| 2.2 | Complete | Preserver types/services, FeatureBundle merge, frozen snapshot accessor, bounded rollback and content-free fail-open metrics | SDK/featurebundle/extensions suites green; `go vet` green |
| 2.3 | Complete | Process-owned BackgroundAux scheduler and lifecycle composition | Auxreq/runtimebundle suites green; module verification and checkptr stress green |

Integrated evidence:

- `go test -count=1 ./internal/core/compactiondetect ./internal/core/runtime ./pkg/lipsdk/... ./internal/featurebundle/... ./internal/core/extensions/... ./internal/core/auxreq ./internal/infra/runtimebundle` — pass.
- `go vet` across all changed Wave 1 packages — pass.
- `go mod verify` — pass; `go.mod` and `go.sum` unchanged.
- `gofmt -l` and `git diff --check` — clean.
- `TestCriticalFileBudgets` and `TestShrinkage_ConnectorOverlayExactMeasured` — pass after isolating BackgroundAux lifecycle from the connector surface.
- Absolute `internal/core` and `internal/infra/runtimebundle` measured-line ratchets remain intentionally deferred to Task 6.4, after the complete reviewed feature surface is known.

Remaining Task 1.1/1.2 proof belongs to the runtime integration wave: no billable submission before successful Open and the exact final release sequence through preservation, committed detector lifecycle, metadata observers, and client release.

## Wave 2 — Detached execution and parent-branch authority

Commit: `41c9245a` (`feat(compaction): isolate detached continuity execution`)

| Task | Status | Reviewed implementation | Verification |
| --- | --- | --- | --- |
| 1.4 | Partial | Trusted SDK-only detached mode, captured content-free parent binding, private child A-leg, parent session authority removal, route-override isolation, child B-leg failover lineage | Auxreq/runtime/B2BUA/secure-session suites green; frontend/header/accounting integration remains in later tasks |
| 2.4 | Complete | Explicit detached auxiliary execution reuses Executor routing, request authority and B2BUA paths while skipping primary secure-session effects | `go test -count=1 ./pkg/lipsdk/auxiliary ./internal/core/auxreq ./internal/core/runtime ./internal/core/b2bua ./internal/core/securesession/...` — pass |
| 2.5 | Complete | Process-owned parent BranchKey coordinator with explicit capture, revision/job/preview/injection consistency, bounded reload-stable state and opaque backing | Coordinator tests `-count=50`, runtimebundle ownership test and `go vet` — pass |

Integration review repairs:

- Detached mode is opt-in; existing auxiliary plugin requests retain default session semantics.
- Parent branch binding is explicit content-free auxiliary metadata and is never derived from canonical session hints or encoded for providers.
- Parent session/client/resume/continuity fields are absent from the private child canonical call and SessionView; parent lineage remains only in trusted execution context.
- Coordinator mutations cannot create authority implicitly; `Capture` is the only branch-creation boundary.
- Coordinator persistence is binding-sorted and persistence failures leave in-memory state unchanged.
- ProcessServices coordinator composition preserves the critical-file and connector-overlay ratchets without whitespace deletion or cap changes.

Environment limitation remains unchanged: targeted race execution cannot start under Windows because ThreadSanitizer exits with allocation error 87.

## Wave 3 — Feature configuration and pure continuity semantics

Implementation commit: `0f981f0e` (`feat(compaction): add continuity feature semantics`)

| Task | Status | Reviewed implementation | Verification |
| --- | --- | --- | --- |
| 1.5 | Partial | Capsule binding/digest, conflict-key precedence, atomic supersession, deterministic pruning, and versioned Codex/OpenCode/Cline carrier fixtures; billing and repeated-compaction integration remain later work | Capsule/carrier suites `-count=20`; parser/carrier fuzz; `go vet` |
| 3.1 | Complete | Official standard feature registration, strict bounded D18-compatible configuration, disabled-by-default semantic extraction, explicit route/inherit validation, immutable value snapshots, and enabled-generation prerequisite gate | Feature/standardplugins/runtimebundle tests and `go vet` — pass |
| 3.2 | Complete | Self-binding capsule v1, typed canonical digest, strict parse/validation, parent-only supersedes references, conflict precedence, stale rejection, whole-fact pruning, and versioned carrier normalization | Pure feature suites `-count=20`; fuzz and lint evidence — pass |
| 3.3 | Partial | Pure bounded canonical source preparation, existing secretguard integration, untrusted tool framing, deterministic incremental watermark, and pay-only eligibility | Source suite `-count=20`, fuzz and `go vet` — pass; successful-Open commit integration remains Task 4.1 |

Integration review repairs:

- Missing or disabled feature registration is a true prerequisite-gate no-op, including with nil process services; enabled registration still fails clearly when any required capability is missing.
- Configuration and sanitizer implementations are split by concern; every new production file is below 330 lines without weakening file-budget checks.
- A losing mixed-authority decision candidate cannot partially supersede prior active decisions, and same-delta/forward supersedes references are rejected rather than treated as parent state.
- Carrier recognition remains a small explicit shape catalog and does not infer agent/provider brands.
- Sanitized source preparation never commits state or starts auxiliary work; those lifecycle effects remain reserved for successful primary Open integration.

Integrated evidence:

- `go test -count=1 ./internal/plugins/features/compactioncontinuity/... ./internal/standardplugins ./internal/infra/runtimebundle` — pass.
- `go vet ./internal/plugins/features/compactioncontinuity/... ./internal/standardplugins ./internal/infra/runtimebundle` — pass.
- `go test -count=1 ./internal/archtest -run "TestCriticalFileBudgets|TestShrinkage_ConnectorOverlayExactMeasured"` — pass.
- `git diff --check` — clean.
- Quality-accounting commit `337fc37e` records the process-owned bounded worker allowlist, package documentation, exact core-import baseline, and measured-plus-25 line ratchets.
- `make quality-checks` — pass after the quality-accounting repair.
