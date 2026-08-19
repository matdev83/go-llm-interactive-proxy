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

## Wave 4 — Semantic extraction and authoritative late-result merge

Implementation commit: `013cc010` (`feat(compaction): add semantic extraction and late merge`)

| Task | Status | Reviewed implementation | Verification |
| --- | --- | --- | --- |
| 1.5 | Partial | Detached child billing proof covers originating account attribution, independent BillingCallID/A-leg/failover legs, positive AttemptSeq, explicit evidence identity, and separation of primary protocol usage from auxiliary usage | Focused runtime/billing/auxiliary suites and `go vet` pass; feature-to-runtime scheduling remains Task 4.1 |
| 3.4 | Complete | One detached no-tools child call with explicit independent route/inherit policy, immutable time/input/output bounds, private lineage and plugin suppression; fixed prompt/schema; strict single-object parser with bounded depth/count/text/source/conflict/supersedes validation and authority-safe decision transitions | Feature suite `-count=20`, parser fuzz, `go vet`, and full quality gate pass |
| 3.5 | Complete, race runner unavailable | Late-result service verifies pending parent job, branch binding, capsule digest/revision, performs bounded merge/prune/re-digest, retains timed-out jobs, forgets terminal raw output, and commits through an adapter bound to the captured authoritative parent BranchKey and real coordinator job CAS | Result-merge stress tests, actual-coordinator adapter tests, integrated feature/runtimebundle suites, and `go vet` pass; Windows ThreadSanitizer remains unavailable with allocation error 87 |

Integration review repairs:

- Extractor child calls remain ordinary internal create calls and cannot recursively identify themselves as compaction operations.
- Input policy now exposes the approved `max_input_tokens` contract and enforces a conservative provider-independent token-equivalent bound plus a fixed hard byte ceiling.
- Source references use exact allowlist membership; stable semantic-ID retries must be identical; protected user, acceptance, and structured decisions cannot be removed by semantic output.
- `remove_or_supersede` results map to typed capsule decision transitions with provenance instead of being discarded or rewriting protected decision fields.
- The late-result adapter never decodes a binding or accepts a private child A-leg as parent authority.
- Missing provider billing evidence receives explicit unavailable provenance and a BillingCallID/B-leg-scoped dedupe identity without replacing provider-reported evidence.

Integrated evidence:

- `go test -count=20 ./internal/plugins/features/compactioncontinuity/...` — pass.
- Focused actual-coordinator adapter and compaction-continuity billing tests `-count=5` — pass.
- `go vet` across feature, runtimebundle, runtime, billing, auxiliary, coordinator, and standard-plugin packages — pass.
- `make quality-checks` — pass, including architecture, convergence, formatting, build, vet, regex, and goroutine guardrails.
- `git diff --check` — clean.

## Wave 5 — Runtime boundary preservation, process composition, and billing isolation

Implementation commits:

- `d8e92779` (`feat(compaction): enforce preservation release lifecycle`)
- `452491df` (`feat(auxreq): bind generation runners and workload role`)
- `467e87d1` (`feat(compaction): preserve continuity across boundaries`)
- `0069cc90` (`feat(billing): project continuity auxiliary workload`)
- `6e4869c1` (`test(arch): account for continuity runtime surface`)

| Task | Status | Reviewed implementation | Verification |
| --- | --- | --- | --- |
| 1.2 | Complete | Additive optional failed-Open and post-finalization release callbacks preserve existing `Preserver` compatibility; transactional response mutation precedes detector commit, metadata dispatch, and the client return point | Runtime/extensions/SDK lifecycle tests `-count=20`; full package tests and vet pass |
| 1.3 | Complete | One process-owned bounded scheduler exposes immutable generation-bound runner views; submissions capture the exact executor and async generation pin without a per-generation pool or fallback goroutine | Auxreq tests `-count=20`, checkptr stress, goleak-backed package suite, and runtimebundle reload tests pass |
| 1.4 | Complete | Detached execution carries a validated trusted auxiliary role, retains private child A-leg/routing/billing lineage, and leaves secure-session/transcript authority on the parent | Runtime detached/session isolation matrix `-count=20` and full runtime suite pass |
| 3.2 | Complete | Capsule pruning now enforces byte and conservative UTF-8 token-equivalent limits together, removes only whole facts, and re-digests deterministically | Capsule suite `-count=20` and vet pass |
| 4.1 | Complete | Successful Open commits source and deterministic capsule state before eligibility, then submits one parent/transaction/revision-coalesced detached job without awaiting it | Feature/runtimebundle integration and delayed-background tests pass |
| 4.2 | Complete | Pure response preview, bounded existing-job barrier, strict parent-bound merge, detector commit on the exact finalized event, metadata dispatch, and release callback are ordered at the single client-release seam | Live/gated/recovery/fail-open ordering tests `-count=20` and full runtime suite pass |
| 4.3 | Complete | Completion-only preview intents are non-billable before Open, bind to a committed transaction only after Open, and use only already-ready or previously submitted state before the primary request opens | Completion-only, failed-Open, failover, near-miss, and committed-transaction fallback tests pass |
| 4.4 | Complete | Canonical legacy/item reinjection is branch/boundary/revision scoped; bounded ephemeral retry markers commit the released watermark only on returned terminal response and preserve pending state on failure/abort | Injection/feature tests `-count=20`, same-revision/two-boundary and aborted-release coverage pass |
| 4.5 | Partial | Opaque/encrypted payloads remain byte-identical and unsupported carriers fall back to pending reinjection; a verified mutable plaintext carrier remains unimplemented | Injection/feature opaque fixtures pass |
| 5.1 | Complete | Trusted auxiliary workload identity flows through normal call/leg usage, terminal evidence, rating joins and operator reports without changing price selection or creating another money path | Billing/metering/runtime/billingstore tests and vet pass |
| 5.2 | Complete | Primary protocol usage, secure-session state, TurnID/activity/resume and client headers remain isolated from concurrent detached extraction; operator/account records retain separate child lineage | Deterministic concurrent session/billing matrix `-count=20` and full runtime suite pass |

Integration review repairs:

- Completion-only request transactions are retained as release metadata only when the pure response preview has no transaction; strict response previews remain authoritative.
- Bare client `Recv` contexts use the stream's authoritative child A-leg for workload lookup while inherited request-authority pointer identity remains unchanged.
- Runtimebundle contains only thin composition wiring; parent authority, scheduler construction and generation-runner adapters live in focused `internal/infra/compactioncompose`, preserving the required convergence reduction.
- Capsule, extractor and reinjection token-equivalent bounds consistently count one UTF-8 byte per conservative unit.
- No plaintext response carrier was guessed: unknown/native/encrypted/opaque completion events are never mutated and use mandatory reinjection fallback.

Integrated evidence:

- Focused combined tests across runtime, extensions, auxreq, execctx, billing, metering, runtimebundle, billingstore, billingcompose, feature packages and compaction SDK — pass.
- `go vet` across the same integrated package set — pass.
- `make quality-checks` — pass, including formatting, build, vet, architecture, exact package budgets, convergence, regex and goroutine guardrails.
- Runtimebundle convergence remains within the existing package ceiling; connector/core accounting uses measured-plus-25 caps.
- Windows race execution remains unavailable because ThreadSanitizer fails allocation with error 87; deterministic count/checkptr/goleak-backed tests are green.
- PR2 changes 93 paths relative to `feat/compaction-continuity-preservation`, below the 100-path repository limit.
