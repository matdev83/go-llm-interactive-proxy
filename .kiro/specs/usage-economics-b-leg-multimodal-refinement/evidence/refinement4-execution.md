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

The full runtimebundle package run remains red only on unrelated existing
baseline failures (`TestRuntimebundle_NoCompleteOwnerCallbackEscapes` fixture
import parsing and three billing host-loop timeouts). The broader archtest run
also retains unrelated dirty-tree baseline mismatches in billing imports and
the usage-record fixture; the focused composition/public guardrails above pass.

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
