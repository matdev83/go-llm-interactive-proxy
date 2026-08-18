# Implementation Ledger

This ledger records reviewed implementation slices. `tasks.md` has no task
checkboxes, so task completion is recorded here without altering its plan text.

## Approval and prerequisite

| Commit | Scope | Evidence |
| --- | --- | --- |
| `600c938e` | Requirements, design, and tasks accepted; spec ready for implementation | `origin/main` at `2b30116c` contains the merged `compaction-event-detection` detector runtime; branch rebase reported up to date; `spec.json` strict JSON parse and diff check passed |

## Wave 1 — Preservation foundations

Commit: `0fba0f71` (`feat(compaction): add continuity preservation foundations`)

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
