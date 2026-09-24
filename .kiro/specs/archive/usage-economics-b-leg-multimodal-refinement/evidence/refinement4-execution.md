# Refinement 4.1 execution evidence

## Scope and provenance

- Worktree: `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy-feat-b-leg-usage-economics`
- Branch: `feat/b-leg-usage-economics`
- Observed HEAD: `6fe6189b5b5426fa3fbeee59fe2bbc63f3002ebf`
- Refinement: `usage-economics-b-leg-multimodal-refinement`, approved and `ready_for_implementation`
- Task: `4.1 Append bounded pre-terminal B-leg economic checkpoints`
- Measurement window: `2026-09-15` UTC
- This pass changes runtime capture and the metering journal adapter only. It does not change billing valuation/settlement or provider adapters, and it does not change Kiro task status, commit, merge, or PR state.

## TDD RED

Before the runtime implementation existed, the focused command
`go test -count=1 ./internal/core/runtime -run '^TestRefinement41'` failed at
compile time because the behavior-first tests referenced the absent
`attemptSession.observationSink`, `flushEconomicCheckpoints`, and bounded queue
entry point. The tests covered pre-terminal visibility, coalescing, terminal
flush/no duplicate, cancellation and journal errors, and absence of money
mutation.

## Implemented behavior

- Attempt-owned V2 checkpoints are queued separately from terminal billing
  evidence. Accepted provider observations are canonicalized and queued after
  source/replay deduplication.
- Cumulative heads coalesce by source identity without moving backwards on
  reordered revisions. Delta, correction, and replacement observations retain
  revision-qualified identities so supersession history is never dropped.
- The pending queue is bounded at 64 entries. Flushes run at a batch size of 8
  or a 250ms cadence, and the `AtomicObservationSink.AppendObservations`
  capability appends every flush batch atomically and idempotently. Flushes are
  serialized so receive and terminal owners cannot append the same pending head
  concurrently.
- Local boundary planes use the same queue. Repeated local cumulative snapshots
  advance immutable revisions only when measurements change; receipt timestamps
  alone do not create a revision. Provider/customer planes remain separate
  observations.
- Terminal processing force-flushes with a cancellation-independent two-second
  timeout. Both terminal billing-leg paths flush after finalizer evidence is
  captured. Checkpoint code calls no valuation, authority, settlement, or money
  mutation function.
- `journalstore.NewObservationSink` adapts the existing V2 observation journal
  to both the compatibility `ObservationSink` and retry-safe
  `AtomicObservationSink` contracts. `AppendObservations` uses one transaction
  with the existing bounded SQLite retry policy. A collision or projection
  error rolls the batch back.
- Configuring an atomic V2 sink enables the attempt's local-boundary capture
  and declines the unmeasurable large-body fast path, preserving the required
  provider-bound observation capability. A compatibility-only sink is not
  treated as a durable checkpoint capability.

## Acceptance coverage

| Acceptance | Executable coverage |
| --- | --- |
| Durable pre-terminal provider evidence is queryable | `runtime.TestRefinement41PreTerminalCheckpointIsQueryableFromDurableStore` |
| Durable pre-terminal local boundary evidence and revision advancement | `runtime.TestRefinement41LocalBoundaryCheckpointIsDurablyVisibleBeforeTerminal` |
| Cumulative coalescing and bounded pending behavior | `runtime.TestRefinement41PreTerminalCheckpointsCoalesceCumulativeSnapshots` |
| Delta batching avoids per-frame sink calls | `runtime.TestRefinement41PreTerminalDeltaCheckpointsUseBoundedBatch` |
| Terminal flush is idempotent and concurrent flushes do not duplicate | `runtime.TestRefinement41TerminalFlushesPendingOnceWithoutDuplicate`, `runtime.TestRefinement41ConcurrentCheckpointFlushDoesNotDuplicate` |
| Cancellation/error retention and no direct money mutation | `runtime.TestRefinement41CheckpointCancellationRetainsPendingAndSinkErrorIsObservable`, `runtime.TestRefinement41PreTerminalCheckpointDoesNotMutateMoney` |
| Generic sink retry safety and atomic capability | `runtime.TestRefinement41GenericSinkWithoutAtomicBatchFailsClosedOnRetry`, `runtime.TestRefinement41AtomicBatchSinkRetriesAmbiguousCommitWithoutDuplicate` |
| Journal batch query visibility, replay, and atomic rollback | `journalstore.TestRefinement41AppendObservationsBatchIsQueryableAndAtomic` |
| Executor injection seam | `runtime.TestRefinement41ExecutorPassesObservationSinkToAttempt` |

## Fresh verification

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 ./internal/core/runtime -run '^TestRefinement41'` | 0 | All refinement 4.1 runtime tests pass. |
| `go test -count=1 ./internal/core/runtime` | 0 | Full runtime package passes. |
| `go test -count=1 ./internal/infra/metering/journalstore -run '^TestRefinement41'` | 0 | Batch/query/rollback test passes. |
| `go test -count=1 ./internal/infra/metering/journalstore` | 0 | Full journalstore package passes. |
| `go vet ./internal/core/runtime ./internal/infra/metering/journalstore` | 0 | Vet passes for owned production packages. |
| `git diff --check` | 0 | No whitespace errors. |
| `go test -race -count=1 ./internal/core/runtime -run '^TestRefinement41'` | 1 | Windows `runtime/cgo` could not build `cgo.exe` (exit status 2); race results are unavailable. The same infrastructure failure also blocked the journalstore race command. |

## Boundary and residual risk

`AccountingRuntime.MeteringObservationSink` is an explicit injection seam. A
composition root that owns a `journalstore.DurableStore` must provide
`journalstore.NewObservationSink(store)` to enable production pre-terminal
durability; nil keeps the existing in-request-only behavior. The compatibility
`ObservationSink.Append` method is never retried by the checkpoint path because
its error may have unknown commit status. A sink without
`AtomicObservationSink` is rejected at flush time and its bounded queue is
retained; no unsafe single-observation write is attempted. Atomic sinks may be
retried after an error because their complete-batch and exact-replay guarantees
prevent duplicate or lost observations. The race gate remains unverified
because of the Windows toolchain failure above.

## Review repair: bounded retry ownership and stale-head fencing

The review follow-up added behavior-first regressions before changing the
runtime: `TestRefinement41FullQueueFailedFlushRetainsAcceptedObservationForRecovery`,
`TestRefinement41RejectedProviderObservationReplaysAfterCapacityRecovery`,
`TestRefinement41LocalCumulativeHeadWaitsForBoundedQueueAdmission`, and
`TestRefinement41StaleCumulativeRevisionAfterSuccessfulFlushIsIgnored`. The focused
RED command was:

`go test -count=1 ./internal/core/runtime -run 'TestRefinement41(FullQueueFailedFlushRetainsAcceptedObservationForRecovery|StaleCumulativeRevisionAfterSuccessfulFlushIsIgnored)$'`

Both tests failed against the prior implementation: the recovered queue wrote
64 instead of 65 accepted deltas, and a stale revision 1 appended after a
durable revision 2.

The minimal repair keeps a second attempt-owned deferred queue bounded to 64
entries. A full pending queue admits new observations there without waiting on
the journal, and a later batch/terminal flush includes both queues. Failed
flushes leave both queues intact. Local boundary revision heads are committed
only after queue admission, while successful cumulative writes update a bounded
durable revision fence so older snapshots cannot be re-appended.

## Review repair verification

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 ./internal/core/runtime -run 'TestRefinement41(FullQueueFailedFlushRetainsAcceptedObservationForRecovery|StaleCumulativeRevisionAfterSuccessfulFlushIsIgnored)$'` | 0 after repair | Both regressions pass; initial RED had both failures described above. |
| `go test -count=1 ./internal/core/runtime -run '^TestRefinement41'` | 0 | All refinement 4.1 runtime tests pass. |
| `go test -count=1 ./internal/core/runtime/... ./internal/infra/metering/journalstore/...` | 0 | Focused runtime and journalstore packages pass. |
| `go test -count=1 -shuffle=on ./internal/core/runtime ./internal/infra/metering/journalstore` | 0 | Shuffled runtime/journalstore tests pass. |
| `go vet ./internal/core/runtime ./internal/infra/metering/journalstore` | 0 | Vet passes. |
| `gofmt -d <owned Go files>` | 0 | No formatting differences. |
| `git diff --check` | 0 | No whitespace errors. |
| `go test -race -count=1 ./internal/core/runtime -run '^TestRefinement41'` and journalstore equivalent | 1 | Windows `runtime/cgo` failed while building `cgo.exe`; race results remain unavailable. |
| `go test -count=1 ./...` | 1 | Existing unrelated arch/baseline failures remain; runtime and journalstore packages passed in the run. |

The repair adds no goroutines, no runtimebundle composition changes, and no
valuation, settlement, or money mutation path. It leaves the existing single
observation sink interface source-compatible while adding the explicit atomic
batch capability required by runtime checkpoint retries.
Pending plus deferred checkpoint state is bounded at 128 entries per attempt;
the durable cumulative fence is bounded by the existing 1024-observation
attempt evidence limit. A sink outage still delays visibility until recovery or
terminal timeout; no retry worker is introduced by this repair.

## Review repair: atomic sink contract and partial-write retry safety

The review follow-up added a behavior-first regression with a generic sink that
successfully appends revision 1, returns an error for revision 2, and then
receives the whole batch again. Before the contract repair, the focused command

`go test -count=1 ./internal/core/runtime -run '^TestRefinement41GenericSinkWithoutAtomicBatchFailsClosedOnRetry$'`

failed because the retry wrote 3 observations for 2 accepted revisions. The
minimal contract repair adds public `metering.AtomicObservationSink`, whose
batch operation is complete-batch atomic, exact-replay idempotent, and safe to
retry after an ambiguous error. Runtime checkpoint flushing now calls only that
capability, including for a one-observation batch; a plain compatibility sink
fails closed without being invoked and keeps its bounded pending queue.

The journalstore adapter statically conforms to both contracts and its one-
transaction batch implementation rolls back on collision/projection errors.
The GREEN regression uses an atomic test sink that commits a full batch then
loses its acknowledgement; retrying the same batch produces exactly one copy
of each revision.

## Atomic sink contract verification

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 ./internal/core/runtime -run '^TestRefinement41(GenericSinkWithoutAtomicBatchFailsClosedOnRetry|AtomicBatchSinkRetriesAmbiguousCommitWithoutDuplicate)$'` | 0 | Non-atomic sink is not invoked; ambiguous atomic commit retries without duplicate/loss. |
| `go test -count=1 ./internal/core/runtime -run '^TestRefinement41'` | 0 | All refinement 4.1 runtime tests pass with the required atomic capability. |
| `go test -count=1 ./internal/infra/metering/journalstore -run '^TestRefinement41'` | 0 | Adapter conformance and atomic rollback/replay pass. |
| `go test -count=1 ./pkg/lipsdk/metering` | 0 | SDK compatibility and atomic capability contracts compile and pass. |
| `go test -count=1 ./internal/core/runtime/... ./internal/infra/metering/journalstore/... ./pkg/lipsdk/metering` | 0 | Full owned runtime, journalstore, and metering SDK package trees pass. |

## Review repair: production runtime composition injects the durable V2 sink

The composition regression was added before the runtimebundle change. The RED
command

`go test -count=1 ./internal/infra/runtimebundle -run '^TestBuildHost_(MeteringJournalInjectsAtomicObservationSinkBeforeTerminal|DisabledMeteringDoesNotAllocateSinkOrJournal)$'`

failed because a normally composed metering-enabled executor exposed no V2
observation sink. The disabled-mode case already passed, proving that the
regression was specifically the missing production injection.

The minimal wiring now keeps `meteringRuntime` as the process-owned composition
record. Durable SQLite/PostgreSQL construction returns the existing
`*journalstore.DurableStore`; the runtimebundle derives the non-owning
`journalstore.NewObservationSink(store)` adapter from that exact recorder and
passes it to `AccountingRuntime.MeteringObservationSink`. The adapter conforms
to both `ObservationSink` and `AtomicObservationSink`, so the attempt checkpoint
path gets the atomic batch contract without a second store or a public money
option. Memory metering and disabled metering leave the sink nil. An injected
compatibility recorder likewise leaves the sink nil because no durable V2 store
is owned by that override.

`processResourceOwner` continues to own the durable store. SQLite closes its
direct database through the existing owner callback; PostgreSQL keeps the
existing shared `PoolRegistry` acquisition/release lifecycle. Candidate
generations receive the process-owned recorder and sink as non-owning refs and
do not close them, so reload cannot double-close the journal. The composition
test closes the host through the standard cleanup path after exercising the
same store that the executor sink writes.

## Production composition verification

| Command | Exit | Result |
| --- | ---: | --- |
| Focused composition test above (before wiring) | 1 | Expected RED: enabled executor sink was nil. |
| `go test -count=1 ./internal/infra/runtimebundle -run '^TestBuildHost_(MeteringJournalInjectsAtomicObservationSinkBeforeTerminal|DisabledMeteringDoesNotAllocateSinkOrJournal)$'` | 0 | GREEN: preterminal V2 evidence is queryable from the owned SQLite journal; disabled mode has nil recorder/sink and no journal file. |
| `go test -count=1 ./internal/infra/runtimebundle -run 'TestBuildHost_(MeteringJournalInjectsAtomicObservationSinkBeforeTerminal|DisabledMeteringDoesNotAllocateSinkOrJournal)|Test(OpenDurableMeteringJournal|ProcessOwner|MeteringPool)'` | 0 | Composition, owner, durable journal, and pool lifecycle tests pass. |
| `go test -count=1 ./internal/infra/metering/journalstore` | 0 | Full journalstore package passes. |
| `go test -count=1 ./internal/core/runtime` | 0 | Full runtime package passes. |
| `go vet ./internal/infra/runtimebundle ./internal/infra/metering/journalstore ./internal/core/runtime` | 0 | Vet passes for owned production packages. |
| `go test -count=1 ./internal/archtest -run '^(TestBillingRuntimeHasNoModeSelectorInProductionComposition|TestPhase1PublicOptionsStayNonMonetary|TestRuntimeBillingBoundaryHasNoStreamMonetarySettlement)$'` | 0 | Production composition and public non-money guardrails pass. |
| `go test -count=1 ./pkg/lipruntime -run 'TestOptions_DoesNotExposeBillingStore|Test.*Metering|Test.*Capability'` | 0 | Public `pkg/lipruntime.Options` remains free of an accounting-store seam. |
| `git diff --check` | 0 | No whitespace errors. |

The phase-level remediation subsequently made the callback architecture and all
three stock billing host-loop tests green. The malformed import originated from
an ignored top-level ` internal` duplicate fixture; the exact duplicate file
was removed. The focused composition/public guardrails above remain green.

## Review repair: rejected-observation replay after in-flight capacity failure

The final blocker regression is covered by
`TestRefinement41RejectedProviderObservationReplaysAfterCapacityRecovery`.
It fills both bounded queues, holds a 128-entry atomic flush in flight until
the sink returns an error, delivers the 129th unique provider delta during that
flush, then recovers capacity and replays it. Before admission-before-dedupe,
the replay was suppressed and only 128 observations became durable. The test
also requires the existing `checkpointErr` channel to expose the bounded
capacity rejection.

`TestRefinement41LocalCumulativeHeadWaitsForBoundedQueueAdmission` verifies
that local cumulative heads remain unchanged when both bounded queues reject
admission and advance only after a successful recovery flush and queue
ownership. The stale-revision regression is named
`TestRefinement41StaleCumulativeRevisionAfterSuccessfulFlushIsIgnored`.

The repair admits the checkpoint before installing the terminal evidence
dedupe marker. A rejected observation therefore remains replayable, while
accepted or intentionally ignored observations retain the existing terminal
evidence semantics. No new goroutine or public/composition seam is introduced;
pending plus deferred checkpoint state remains bounded at 128 entries.

| Command | Exit | Result |
| --- | ---: | --- |
| Focused blocker regression before admission repair | 1 | RED: replay persisted 128 instead of the required 129 observations. |
| `go test -count=1 ./internal/core/runtime -run '^TestRefinement41(RejectedProviderObservationReplaysAfterCapacityRecovery|LocalCumulativeHeadWaitsForBoundedQueueAdmission|StaleCumulativeRevisionAfterSuccessfulFlushIsIgnored)$'` | 0 | GREEN: rejected replay, local-head admission, and stale fence pass. |
| `go test -count=20 ./internal/core/runtime -run '^TestRefinement41'` | 0 | Repeated refinement runtime tests pass. |
| `go test -count=1 -shuffle=on ./internal/core/runtime ./internal/infra/metering/journalstore` | 0 | Shuffled runtime/journalstore tests pass. |
| `gofmt -d <owned Go files>` / `go vet ./internal/core/runtime` / `git diff --check` | 0 | Formatting, vet, and whitespace checks pass. |

The race gate remains unavailable on this Windows host because `runtime/cgo`
cannot build `cgo.exe`; no race result is claimed.

## Review repair: observable bounded checkpoint rejection

The generic-sink evidence command now names the actual regression,
`TestRefinement41GenericSinkWithoutAtomicBatchFailsClosedOnRetry`.
`TestRefinement41CheckpointCapacityRejectionLogsOnceThroughRuntimeDiagnostics`
drives two rejected provider observations through `drainSidebandEvidence` and
asserts one structured `economic_checkpoint_capacity_rejected` signal with the
stable `checkpoint_capacity_exhausted` reason. The signal carries only the
bounded semantics/revision context and existing attempt diagnostic lineage; it
does not log the provider source-event key. An admitted observation emits no
capacity-rejection signal.

The attempt retains one pending/emitted diagnostic slot under `checkpointMu`.
Queue admission still records `checkpointErr`, never logs or mutates money from
the receive callback, and repeated capacity failures are coalesced per attempt.

| Command | Exit | Result |
| --- | ---: | --- |
| Focused diagnostic regression before implementation | 1 | RED: no production capacity-rejection signal was emitted. |
| `go test -count=1 ./internal/core/runtime -run '^TestRefinement41CheckpointCapacityRejectionLogsOnceThroughRuntimeDiagnostics$'` | 0 | GREEN: one coalesced rejection signal, no source-event leakage, and no false signal on admitted evidence. |
| `go test -count=1 ./internal/core/runtime` / `go test -count=1 ./internal/core/runtime/...` | 0 | Full runtime packages pass. |
| `go test -count=1 ./internal/infra/metering/journalstore` | 0 | Full journalstore package passes. |
| `go test -count=20 ./internal/core/runtime -run '^TestRefinement41'` / `go test -count=1 -shuffle=on ./internal/core/runtime ./internal/infra/metering/journalstore` | 0 | Repeated and shuffled focused/runtime/journalstore checks pass. |
| `go vet ./internal/core/runtime ./internal/infra/metering/journalstore` / `gofmt -d <owned Go files>` / `git diff --check` | 0 | Vet, formatting, and whitespace checks pass. |

The focused race build remains unavailable on this Windows host because
`runtime/cgo` cannot build `cgo.exe`; no race result is claimed.

## Refinement 4.2 execution evidence: revision-keyed pure valuation

The 4.2 seam reuses the immutable `billing_economic_work` queue and existing
V2 valuation/reconciliation persistence. `EconomicRevisionWork` is normalized
from a B-leg subject and provider-neutral rating input; its identity is the
SHA-256 key over queue, head key, evidence revision, and canonical input-set
hash. Valuation and reconciliation IDs derive from that key. A changed input
hash or later evidence revision therefore creates a new immutable result, while
repeated delivery of the same revision is a no-op (including transport-only
receipt timestamp changes).

Each pure worker is bound to exactly one queue (`customer` or `provider`). It
rates and optionally reconciles outside persistence, then atomically appends
only valuation, reconciliation, and the rebuildable current-head pointer. The
result transaction does not call account, exposure, journal, settlement, or
customer-unit APIs. A durable result/head probe prevents a restarted worker
from re-rating an already complete revision and repairs a missing head by
replaying the immutable result.

The additive head projection is dual-dialect SQLite/PostgreSQL and stores the
queue, head key, revision/input identity, result references, fingerprints,
head version, and fence. A stale/reordered revision cannot regress a newer
head; equal-revision corrections converge by canonical input-hash ordering.

| Command | Exit | Result |
| --- | ---: | --- |
| RED compile state for the new revision worker/store tests | 1 | Tests were authored against the absent revision identity, queue, result, and head seams before implementation. |
| `go test -count=1 ./internal/core/metering/replay ./internal/core/billing ./internal/infra/billingstore` | 0 | Core identity/worker and durable SQLite revision queue, valuation, reconciliation, replay, head, and no-balance-mutation tests pass. |
| `go test -count=20 -shuffle=on ./internal/core/billing -run '^TestEconomicRevisionWorker_'` | 0 | Repeated worker tests pass for preterminal, terminal replay, late correction, queue isolation, and reordered/restart convergence. |
| `go test -count=1 ./internal/infra/billingstore -run '^TestRefinement42'` | 0 | Durable duplicate/reordered revision and transport-metadata replay tests pass; pure results leave journal and unit-balance tables unchanged. |
| `go test -count=1 ./internal/infra/runtimebundle -run 'TestBuild|TestCompose|ProcessBilling'` | 0 | Selected production composition and process-worker wiring tests pass. |
| `go test -count=1 ./internal/core/metering/replay ./internal/core/billing ./internal/infra/billingstore ./internal/infra/runtimebundle` | 1 at initial checkpoint | Replay, billing, and billingstore passed; later phase-level remediation fixed the runtimebundle malformed-import artifact and host-loop closure regressions, with the focused stock gates passing repeatedly. |
| `make test-db-parity-sqlite` | 0 | SQLite dialect parity passes, including billingstore migration/schema checks. |
| `go vet ./internal/core/metering/replay ./internal/core/billing ./internal/infra/billingstore ./internal/infra/runtimebundle` | 0 | Vet passes for owned Go packages. |
| `gofmt -d <owned Go files>` / `git diff --check` | 0 | Formatting and whitespace checks pass. |
| `go test -race ...` | 1 | Windows `runtime/cgo` cannot build `cgo.exe`; no race result is claimed. |

The direct PostgreSQL parity run reached the billingstore component
successfully; the repository-wide run later failed in unrelated conversationview
network setup. The queue intentionally retains immutable pending
history and uses a result probe; mutable claim/retirement state is recorded in
the revision queue state table. Upstream 4.1 evidence producers must
enqueue a normalized revision through `AppendEconomicRevisionWork`; this task
does not add stream-callback journal coupling or incremental monetary posting.

## Refinement 4.2 queue-progress repair

The bounded pure worker now keeps mutable delivery state in
`billing_economic_revision_work_state`, separate from immutable
`billing_economic_work`. A queue page excludes completed work and live leases,
so completing an early page cannot starve later revisions or a newly appended
late correction. Failed attempts return to `pending` with their error and are
due immediately for retry. Claims carry a finite lease and monotonically
increasing fence; completion/retry compare both owner and fence, allowing an
expired claim to be recovered while rejecting stale workers. Results and heads
remain durable if a process stops between result commit and state retirement.

| Command | Exit | Result |
| --- | ---: | --- |
| Focused queue-progress/failure tests before state implementation | 1 | RED: bounded-page and retry tests failed because the durable state table did not exist. |
| `go test -count=1 ./internal/infra/billingstore -run '^TestRefinement42RevisionQueue'` | 0 | GREEN: completed first page is retired, late correction is eventually rated/headed, failed work retries, and immutable rows/results remain present. |
| `go test -count=20 -shuffle=on ./internal/core/billing -run '^TestEconomicRevisionWorker_'` / `go test -count=10 -shuffle=on ./internal/infra/billingstore -run '^TestRefinement42'` | 0 | Repeated and shuffled pure-worker/revision tests pass. |
| `make test-db-parity-sqlite` | 0 | SQLite migration and schema parity pass for the queue-state table/index. |
| `$env:LIP_REQUIRE_POSTGRES='1'; go test -tags integration -count=1 ./internal/infra/billingstore -run '^TestDBParity_PostgresDirect$'` | 0 | Focused direct PostgreSQL billingstore schema/contract parity passes. |
| `go vet ./internal/core/metering/replay ./internal/core/billing ./internal/infra/billingstore ./internal/infra/runtimebundle` | 0 | Vet passes for the owned packages. |

The focused direct PostgreSQL billingstore parity wrapper passes; the aggregate
direct run later timed out in unrelated conversationview setup. The state DDL
and queries use the existing SQLite/PostgreSQL migration pattern.
The race build remains unavailable on this Windows host because `runtime/cgo`
cannot build `cgo.exe`; no race result is claimed.

## Refinement 4.2 review repair: reject valuation input-set mismatch at the worker boundary

The review blocker regression was authored before the production repair as
`TestEconomicRevisionWorker_RejectsMismatchedValuationInputBeforeReconcileOrPersist`.
It supplies a valid rater valuation whose canonical observation references and
matching supplied hash describe a different input set than `work.InputSetHash`.
A permissive result store accepts any output, so the test proves the core
worker—not a durable adapter—rejects the result. A stateful fake queue also
requires one retry/release and no completion; reconciliation and persistence
must both remain untouched.
The companion
`TestEconomicRevisionWorker_RejectsEmptyValuationInputReferences` regression
confirms that an omitted reference set cannot use the approved empty-hash
compatibility to bypass canonical comparison.

The focused RED command was:

`go test -count=1 ./internal/core/billing -run '^TestEconomicRevisionWorker_RejectsMismatchedValuationInputBeforeReconcileOrPersist$'`

RED: the pre-repair worker returned nil and would have passed the mismatched
valuation to the permissive result store.

The minimal GREEN repair adds the core-domain
`EconomicRevisionInputMismatchError`, which unwraps to the existing
`ErrEconomicRevisionInputMismatch`, and compares the valuation's canonical
input-set hash with normalized work before reconciliation or result-store
append. Existing empty supplied hash behavior remains supported by the
approved canonical-input helper; an empty or non-matching canonical reference
set fails closed and is retried through the existing queue-state contract.

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 ./internal/core/billing -run '^TestEconomicRevisionWorker_(RejectsMismatchedValuationInputBeforeReconcileOrPersist|RejectsEmptyValuationInputReferences)$'` | 0 | Typed mismatch is returned; reconciler/store are untouched; claim is retried and not completed for both supplied and omitted reference sets. |
| `go test -count=1 ./internal/core/billing` | 0 | Full core billing package passes. |
| `go test -count=20 -shuffle=on ./internal/core/billing -run '^TestEconomicRevisionWorker_'` | 0 | Repeated/shuffled worker regressions pass. |
| `go vet ./internal/core/billing` | 0 | Vet passes. |
| `gofmt -d internal/core/billing/economic_revision.go internal/core/billing/economic_revision_worker.go internal/core/billing/economic_revision_worker_test.go` | 0 | No formatting differences. |
| `git diff --check` | 0 | No whitespace errors. |

Compatibility impact is limited to rejecting previously-accepted malformed
worker output whose canonical evidence set did not match the durable revision;
the existing sentinel remains compatible through `errors.Is`, and typed
details are available through `errors.As`.

## Refinement 4.2 review repair: deterministic derived timestamps

The review blocker regressions are `TestEconomicRevisionWorker_RetryConvergesAcrossDerivedTimestamps` and `TestEconomicRevisionWorker_DuplicateWorkersConvergeAcrossDerivedTimestamps`, with durable SQLite coverage in `TestRefinement42RevisionRetryConvergesAcrossDerivedTimestamps` and `TestRefinement42DuplicateWorkersConvergeAcrossDerivedTimestamps`. Each rater and reconciler invocation supplies a different nonzero wall-clock timestamp for the same immutable work identity. The retry sink commits the first result before returning an ambiguous error; the duplicate-worker reader presents the same pending marker to two independently constructed workers.

The focused RED command was:

`go test -count=1 ./internal/core/billing -run 'TestEconomicRevisionWorker_(RetryConvergesAcrossDerivedTimestamps|DuplicateWorkersConvergeAcrossDerivedTimestamps)$'`

RED: the pre-repair worker preserved calculator-supplied `CreatedAt` values, so the second attempt failed with `billing: economic revision conflict` after the first result had been recorded.

The minimal GREEN repair always assigns normalized `EconomicRevisionWork.CreatedAt` to both derived `Valuation.CreatedAt` and `EconomicReconciliation.CreatedAt` before validation and result persistence. This is the immutable work time for the revision; zero work times already normalize to the UTC Unix epoch. Snapshot `EffectiveAt`/`FetchedAt` fields remain untouched because they are source snapshot metadata under the approved contracts. Queue/head/revision/input-hash identity remains unchanged, so a later evidence revision still produces a distinct result.

| Command | Exit | Result |
| --- | ---: | --- |
| `go test -count=1 ./internal/core/billing -run 'TestEconomicRevisionWorker_(RetryConvergesAcrossDerivedTimestamps|DuplicateWorkersConvergeAcrossDerivedTimestamps)$'` | 0 | Core retry and duplicate-worker results converge byte-identically while both calculators vary timestamps. |
| `go test -count=1 ./internal/infra/billingstore -run '^TestRefinement42(RevisionRetryConvergesAcrossDerivedTimestamps|DuplicateWorkersConvergeAcrossDerivedTimestamps)$'` | 0 | SQLite durable valuation/reconciliation replay converges; each immutable row remains single-instance and is anchored to work time. |
| `go test -count=1 ./internal/core/metering/replay ./internal/core/billing ./internal/infra/billingstore` | 0 | Full owned replay, billing, and durable-store packages pass. |
| `go test -count=20 -shuffle=on ./internal/core/billing -run '^TestEconomicRevisionWorker_'` / `go test -count=10 -shuffle=on ./internal/infra/billingstore -run '^TestRefinement42'` | 0 | Repeated and shuffled worker/revision regressions pass. |
| `go vet ./internal/core/billing ./internal/infra/billingstore` | 0 | Vet passes for owned packages. |
| `gofmt -d internal/core/billing/economic_revision.go internal/core/billing/economic_revision_worker.go internal/core/billing/economic_revision_worker_test.go internal/infra/billingstore/economic_revision_store.go internal/infra/billingstore/refinement4_2_revision_worker_test.go` | 0 | No formatting differences. |
| `git diff --check` | 0 | No tracked whitespace errors. |
