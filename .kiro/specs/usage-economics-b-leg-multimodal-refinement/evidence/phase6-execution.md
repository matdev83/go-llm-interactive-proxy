# Phase 6 execution evidence

Status: APPROVED

## Scope

Implemented the local boundary measurement slice for parent specification tasks 6.1-6.4 and the overlapping multimodal refinement tasks 3.1-3.3. The change covers request-scoped, provider-neutral B-leg input/output observations, their attempt-owned terminal drain, and fast-path capability gating. Customer-ingress evidence remains separate from final provider-bound input; provider-origin output remains separate from customer egress.

The implementation deliberately excludes the parent connector ABI, provider-specific economics parsing or migrations, generic rating/reconciliation, public host/cutover work, and refinement revision/rating groups. No stream-time rating, journal I/O, or monetary ledger writes were added.

## TDD evidence

RED was established before the production boundary API existed:

```text
go test ./internal/core/metering -run TestPhase6Boundary -count=1
```

Result: expected compile failure. The four new tests reported undefined `NewBoundaryAccumulator`, `BoundaryConfig`, `PreparedInputSummary`, `MediaSummary`, `MediaAudio`, and `ObservationIdentity` symbols.

GREEN after the minimal implementation and runtime/adapter wiring:

```text
go test ./internal/core/metering -run TestPhase6Boundary -count=1
go test ./internal/core/metering ./internal/core/runtime -run TestPhase6 -count=1
go test ./internal/core/metering/... ./internal/core/runtime/... -count=1
go test ./internal/plugins/backends/openaicompat/... ./internal/plugins/backends/openailegacy/... ./internal/plugins/backends/openairesponses/... ./internal/plugins/backends/protocols/anthropicmessages/... ./internal/plugins/backends/protocols/geminigenerate/... -count=1
```

Result: all passed. Runtime tests cover attempt-owned one-shot terminal draining, trusted StoreID/BLeg identity, no-transaction parallel ownership propagation, separate durable observations, pre-assessor required-capability gating, and zero allocation when capture is disabled. Metering tests cover adapter-final input replacement, prepared-only/open-failed/open-accepted lifecycle states, durable replay stability, chunk-invariant output, bounded multimodal capture, context attachment, and honest unavailable measures. Backend tests include a real Anthropic `Backend.Open` request that reaches a test upstream and records transformed SDK fields, plus final-parameter coverage for the OpenAI legacy, OpenAI Responses, Gemini, and OpenAI-compatible wire adapters.

## Implementation evidence

- `internal/core/metering/boundary.go` provides the bounded accumulator and V2 observation conversion. It records final prepared input, provider-origin output, and customer output independently; bounds text/media entries and values; deep-copies mutable snapshots; carries prepared/attempted/accepted state into durable V2 lifecycle measures; and emits estimates with explicit method/schema references when exact provider units are unavailable.
- `internal/core/runtime/local_boundary.go` owns the request-scoped runtime seam and trusted B-leg identity. The terminal drain is attempt-owned and idempotent, and it never performs money or journal work.
- `internal/core/runtime/attempt_session.go`, `billing_leg.go`, and `response_pipeline_observations.go` connect the accumulator to the Phase 5 terminal drain and durable call-leg evidence without replacing provider-derived evidence.
- `internal/core/runtime/executor_open_attempt.go` attaches the required final-input capability before `Backend.Open`, marks attempt lifecycle transitions, and exposes a context callback for adapters after payload construction. `createSessionForParallelLeg` preserves the already-owned boundary, request/store/BillingCallID identity, and scope when a parallel leg has no transaction object.
- `internal/core/runtime/executor_execute_large_body.go` applies the required evidence-capability check before the large-body fast assessor, preserves the unsupported fallback, and wraps eligible wire streams with bounded local observation.
- Essential backend adapters attach provider-neutral prepared-payload summaries through the context seam (`openairesponses`, `openailegacy`, `anthropicmessages`, `geminigenerate`, and `openaicompat` wire). The four SDK adapters walk their final provider parameter trees directly after translation/defaults; the wire adapter reports only bounded final request metadata. No accounting-only JSON body clone is introduced.

## Acceptance mapping

| Acceptance area | Evidence |
| --- | --- |
| Final provider-bound input | The context callback is installed immediately before `Backend.Open`; the four SDK adapters invoke it after constructing their final SDK parameter trees, and the wire adapter invokes it after final HTTP request construction. Real Anthropic integration plus OpenAI legacy/Responses/Gemini parameter tests prove provider-bound data/defaults (including transformed media fields) change the summary while canonical ingress remains unchanged. Text, payload, tool, image/audio/video/document properties, lifecycle, truncation, and method reference are bounded under `backend_egress`; unknown exact units remain estimated or unavailable. |
| Provider-origin output and customer egress | Runtime observes provider events before customer projection and customer events afterward. Separate `backend_ingress` and `frontend_egress` observations are emitted. Cumulative bounded text accounting is invariant to arbitrary event chunking; media values are not replaced with downstream values. |
| Honest unobservables | Cache disposition, hidden reasoning, provider tools, compute, and unsupported exact units are emitted as unavailable/unknown rather than fabricated local values. Provider-derived count evidence remains on its provider origin; no converter clones it as local evidence. |
| Fast path and no-money contract | Required durable metering capability is checked before assessor/commitment and declines unsupported lanes safely. Disabled capture returns no accumulator; the hot seam has a zero-allocation `testing.AllocsPerRun` guard and no observer callback/accounting I/O. Enabled capture is bounded. Optional observation sinks remain independent of money writes, preserving existing #532/#503 behavior. |
| Phase 3-5 integration and ownership | Existing Phase 3 authorized backend-ingress holder is untouched. Local observations use Phase 4 V2 envelope identity and Phase 5 attempt-owned terminal drain, including trusted StoreID, B-leg, attempt sequence, and idempotence. |

## Verification

Passed:

```text
go test ./internal/core/metering/... ./internal/core/runtime/... -count=1
go test ./internal/plugins/backends/openaicompat/... ./internal/plugins/backends/openailegacy/... ./internal/plugins/backends/openairesponses/... ./internal/plugins/backends/protocols/anthropicmessages/... ./internal/plugins/backends/protocols/geminigenerate/... -count=1
go test ./internal/plugins/frontends/frontendpipe -run 'TestFindingB1_|TestLargePayloadBlocker_ZeroTempFilesAndNoScanner|TestLargePayloadHeap_AcceptedWireHasNoCallTree' -count=1
go vet ./internal/core/metering/... ./internal/core/runtime/... ./internal/plugins/backends/...
go test ./internal/archtest -run 'TestArch_StaticDisposition|TestArch_WirePostCommit|TestLargeBodyDoesNotImportProvidersOrFrontends|TestFrozenFactsNoFallbackRatchet' -count=1
go test ./internal/archtest -run 'TestRuntimeStreamHandlersStayOffJournalRatingSettlement|TestPhase51HoldDeletionAndNoStreamMoneyRatchetsStayActive|TestRuntimeBillingBoundaryHasNoStreamMonetarySettlement' -count=1
go test ./internal/plugins/frontends/frontendpipe -run 'TestFindingB1_|TestLargePayloadBlocker_ZeroTempFilesAndNoScanner|TestLargePayloadHeap_AcceptedWireHasNoCallTree' -count=1
make parity-checks
```

`make parity-checks` was rerun with task-scoped Go/build temp directories on `E:` and passed after the first default-temp attempt hit the nearly-full `C:` Windows linker volume. The final focused metering/runtime/adapter run after the review repair also passed. `gofmt` and `git diff --check` passed.

## Skipped or baseline failures

- `go test -race ./internal/core/metering -run TestPhase6Boundary -count=1` could not build because the installed Windows `cgo.exe` exited with status 2; no race result is claimed.
- One initial combined focused run transiently hit the existing `TestBlocker1_WireOwnershipTransfer_CancelALegPromptlyCancelsBackendStreamAndSingleTerminalOutcome` stale-winner assertion. The isolated test and the complete combined metering/runtime/adapter rerun passed; no Phase 6 assertion failed.
- A fresh `go test ./internal/plugins/frontends/... -count=1` sweep was attempted. It exposed existing frontendpipe concurrency/baseline failures (`TestCandidateProof_*`, `TestKeepalive_*`, `TestResponseContext_*`, and saturation-race cases, including an intermittent nil-pointer panic); the directly relevant large-payload/customer-boundary guard command above passed, and no frontend production file is changed by this phase.
- The full `./internal/archtest` suite reports pre-existing branch-wide guard failures (request-attempt-state count/reads, an unrelated economics `Rater` contract, dirty-file count, internal/core complexity, and malformed `GOWORK=off` module-graph handling). The focused relevant architecture guards above pass; no gate was weakened.
- Full `make test`/`make qa` was not rerun because the requested scope is narrow and the targeted package, adapter, frontend, architecture, vet, and parity checks passed.

## Files changed

Production: `internal/core/metering/boundary.go`; `internal/core/runtime/{attempt_session.go,billing_leg.go,executor_execute_large_body.go,executor_open_attempt.go,local_boundary.go,response_pipeline_observations.go}`; and the five essential adapter boundary attachments/helpers in `internal/plugins/backends/`.

Tests: `internal/core/metering/boundary_test.go`, `internal/core/runtime/local_boundary_test.go`, the four SDK-adapter `prepared_boundary_test.go` files, and `internal/plugins/backends/openaicompat/wire_proof_test.go`.

Evidence: this file.

## Residual risks

- Exact provider units and hidden/provider-side economics remain unavailable unless a provider-derived count or an adapter supplies an explicit neutral summary. The local fallback is intentionally labelled as an estimate.
- Wire-only adapters currently provide exact bounded payload byte metadata; richer post-transform media properties require the adapter to invoke the neutral context callback.
- The local accumulator retains bounded summaries only; it intentionally does not retain raw request/output payloads, perform rating, or write a journal.

Root review is APPROVED; see `phase6-review.md`.
