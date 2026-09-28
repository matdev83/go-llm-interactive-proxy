# Refinement 5.1 execution evidence

Status: GREEN for the complete four-artifact Task 5.1 candidate set after the final review remediation. This evidence is bounded to the three owned tests and this evidence document; it does not claim unrelated refinement work complete.

Scope: Task 5.1, “Preserve local call closure while keeping A-leg open-ended.”

Requirements covered: 3.1, 3.2, 3.3, 3.5, 3.6.

Production paths under test: `Executor.Execute` and `lipapi.Collect`, secure-session/B2BUA allocation, durable terminal B-leg and call append, host post-turn settlement, durable exposure closure, provider-cost worker state, and resumable-session billingstore lifecycle. No production source file was changed in this pass.

The complete candidate set is:

- `internal/infra/runtimebundle/refinement5_1_cancellation_billing_integration_test.go`: authentic host/runtime cancellation, fixed customer fee, provider-cost worker completion, and no-phantom-economics assertions.
- `internal/core/runtime/refinement5_1_resumable_session_test.go`: authentic core executor cancellation, bounded stream cleanup, and no-phantom-economics assertions.
- `internal/infra/billingstore/refinement5_1_resumable_session_billing_test.go`: durable resumable-session settlement coverage and the corrected cross-test cancellation comment.
- `.kiro/specs/usage-economics-b-leg-multimodal-refinement/evidence/refinement5-1-execution.md`: this consolidated execution record.

## 1. TDD record and provenance

The tests were tightened in the following order: deterministic completion and bounded cleanup were added first, then complete token/cost/carrier assertions; focused tests ran RED where the first broad measure assertion exposed the legitimate local tokenizer measure; the helper was minimally narrowed to the exact provider token contract; focused tests ran GREEN; formatting and broader verification followed.

### Reconstructed RED provenance labels

`RED-RECONSTRUCTED-LOCAL-TOKEN-MEASURE`: no raw terminal artifact was preserved, so dynamic IDs and timings are intentionally omitted. After the first broad no-token-measure assertion, both cancellation tests failed on the actual local tokenizer observation:

```text
runtimebundle/core cancellation failure: key={Direction:input Component:text_token Unit:token SchemaID: Dimensions:[]} value=5/0
```

This is a reconstructed provenance label, not a captured terminal log. The failure identified that `metering.ComponentTextToken` from the local tokenizer is a legitimate customer-boundary service observation, while the canonical provider inference token components must remain zero. The minimal correction narrowed the assertion to the exact production provider token component keys and retained explicit carrier checks.

`RED-RECONSTRUCTED-IDENTITY`: an earlier observation assertion compared the request ID with the BillingCallID. That was a reconstructed review-gap label only; production uses distinct request and BillingCallID fields. The tests now take the stamped request ID from `call.ID`, the durable BillingCallID from the closed call, and compare each exact production `SubjectRef` and `CorrelationV2` field.

`RED-RECONSTRUCTED-KEYED-NOTIFIER`: before the final wrapper implementation, the test contract changed from `chan struct{}` to a keyed `chan string` and supplied a cancelable notification context. The focused compile then failed because the wrapper still exposed the old channel type and had no notification-context field. This is a reconstructed provenance label, not a captured terminal log. The minimal GREEN implementation now sends `work.Leg.Key` only after the durable defer returns and makes the send cancelable during teardown.

### Historical review-gap notes

The earlier review-gap failures from the wider refinement work are retained only as reconstructed notes; no raw terminal transcripts were preserved. They are not current verification results and are not used to claim completion here:

- An earlier sink assertion manufactured `ObservationRefs`, obscuring the runtime/valuation handoff boundary.
- An earlier immutability check used a synthetic A-leg instead of mutating the actual resumed Call 2 record.
- An earlier cancellation check used a manually simulated billingstore path instead of an authentic host execution.
- An earlier sequence check assumed a fixed sequence rather than the production-allocated attempt sequence.

## 2. Authentic cancellation behavior

The owned test injects a blocking `lipapi.ManagedEventStream`, waits until its `Recv` method has started, then cancels the request context. It verifies that:

- `lipapi.Collect` returns an error matching `context.Canceled` and the backend opens exactly once.
- The durable call outcome is `billing.TurnOutcomeCanceled`.
- Exactly one durable B-leg exists with `billing.LegOutcomeCanceled`.
- The B-leg, closed call, attempt record, and complete-call result retain the same A-leg, B-leg, call, and attempt sequence lineage.
- The test does not retire or replace the A-leg; it verifies that closure records retain the A-leg lineage while the local call exposure closes.

The runtimebundle test owns a `collectDone` channel for `lipapi.Collect`, selects on backend `started`, collection completion, and a five-second startup context, then cancels immediately after the backend starts. A `t.Cleanup` callback cancels and joins the collection goroutine with a five-second context-aware bound on every failure path; the success path performs the same bounded join explicitly. The core runtime test removes the former watcher goroutine entirely and uses the same owned completion channel, bounded startup select, immediate cancellation, and cleanup join. The cancellation/start/join synchronization added by this task is event-driven and bounded; it does not use sleeps or wall-clock polling.

Provider-cost state in the runtimebundle test is synchronized through the test-only `refinement51ProviderCostStore` wrapper. Its unbuffered `chan string` notification carries the committed `billing.CallLegUsageRecord.Key` (the exact string produced by `billing.CallLegUsageKey`) and is sent only after production `DurableStore.DeferProviderCostWork` returns successfully, after the durable transaction commits. The test ignores unrelated keys, waits for the exact sealed leg key, and then reads `GetProviderCostWorkState` once using that same key. Teardown cancellation releases a sender if the bounded wait exits early. The existing host-loop durable-closure helper `waitBillingHostLoopCall` remains bounded ticker polling; that pre-existing closure wait is separate from the event-driven cancellation/start/join and provider-state synchronization added here.

## 3. Fixed customer fee and no phantom provider economics

The configured catalog deliberately includes a fixed request fee of 10 nano in USD. Cancellation after B-leg start therefore has a legitimate fixed call charge; the proof is about no phantom token/provider economics, not a waived customer fee.

Both canceled-leg assertions use the exact production `billing.FinalBillingEvidence` quantity carriers and fail on any nonzero value in `InputTokens`, `OutputTokens`, `ReasoningTokens`, `CacheReadTokens`, `CacheWriteTokens`, or `TotalTokens`. They also require zero `Evidence.Cost.NanoUnits`, no `ObservationRefs`, `EvidenceConflicts`, or `EconomicDispositions`, and no observation `Charges` or `Evidence`.

Observation measures are checked with `metering.Decimal.Normalize` against the canonical production provider token component keys: `input_token`, `input_token_uncached`, `input_token_total`, `cache_read_input_token`, `cache_write_input_token`, `output_token`, `reasoning_output_token`, and `total_token`. Any nonzero canonical provider token measure fails, including nonzero input, cache, output, reasoning, or total measures. The local `text_token` measure is deliberately retained as a local tokenizer/customer-boundary service observation and is not treated as provider inference economics; its presence was the meaningful RED result above.

The runtimebundle test additionally proves the legitimate configured fixed request fee remains exactly 10 nano-units in USD: one customer settlement, one account-version increment, and the corresponding balance debit. That fixed fee is not a token charge. It rejects any `provider_call_cogs` financial journal transaction and any phantom provider cost.

## 4. Durable provider-cost state and report

The test seals the actual canceled `CallLegUsageRecord`, reloads it with production `GetCallLegUsage`, and confirms that the key resolves to the closed call’s actual `BillingCallID` and B-leg ID. It then calls production `GetProviderCostWorkState` for that sealed leg key. This deliberately avoids the due-only `ListPendingProviderCostWork` query.

The direct GREEN assertions require provider-cost state `pending`, attempt count `1`, nonzero retry metadata, and last error `billing: provider cost is unreconciled: provider_evidence_unavailable`. They also require one unreconciled operator-report cost with zero provider cost and the matching `unreconciled_cost` issue. A silently completed row, a missing row, a mismatched leg key, a different failure reason, or a missing report diagnostic fails the test.

## 5. Exact local observations

The canceled leg must contain exactly three observations, and the test rejects any other boundary or non-local origin:

| Boundary | Perspective | Authority |
| --- | --- | --- |
| `metering.BoundaryBackendEgress` | `metering.PerspectiveOperator` | `metering.AuthorityObservedClaim` |
| `metering.BoundaryBackendIngress` | `metering.PerspectiveOperator` | `metering.AuthorityUnavailableClaim` |
| `metering.BoundaryFrontendEgress` | `metering.PerspectiveCustomer` | `metering.AuthorityUnavailableClaim` |

For each observation, the test asserts production `OriginLocal`, `AcquisitionLocalMeasurement`, `LifecycleBackendAttempt`, and `coremetering.BoundaryMappingRef`. The complete `SubjectRef` and `CorrelationV2` are compared against the actual store ID, stamped request ID, BillingCallID/call ID, A-leg ID, B-leg ID, attempt ID, and production attempt sequence. Provider account/request/charge fields are empty, `Charges` and `Evidence` are empty, and no remote/provider observation is accepted.

## 6. Exposure correlation

The returned `CallExposure` must have the actual account ID and closed BillingCallID, `billing.ExposureClosed`, and a non-zero `ClosedAt`. Existing complete-call and leg correlation assertions remain in place.

## 7. Verification commands and results

All commands below were run directly in the mandatory worktree after the final edits.

Focused 20x shuffled matrix:

```powershell
go test ./internal/infra/runtimebundle -run '^TestRefinement51' -count=20 -shuffle=on
```

Result: PASS, exit code 0, 20 iterations (`ok .../internal/infra/runtimebundle`, 20.901s).

```powershell
go test ./internal/infra/billingstore -run '^TestRefinement51' -count=20 -shuffle=on
```

Result: PASS, exit code 0, 20 iterations (`ok .../internal/infra/billingstore`, 0.726s).

```powershell
go test ./internal/core/runtime -run '^TestRefinement51' -count=20 -shuffle=on
```

Result: PASS, exit code 0, 20 iterations (`ok .../internal/core/runtime`, 0.182s).

Full requested package suite:

```powershell
go test ./internal/core/runtime/... ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/... -count=1
```

Result: PASS, exit code 0. Requested runtime, billing, billingstore, and runtimebundle package trees all passed.

Vet:

```powershell
go vet ./internal/core/runtime/... ./internal/core/billing/... ./internal/infra/billingstore/... ./internal/infra/runtimebundle/...
```

Result: PASS, exit code 0, no diagnostics.

Formatting:

```powershell
gofmt -d internal/infra/runtimebundle/refinement5_1_cancellation_billing_integration_test.go internal/core/runtime/refinement5_1_resumable_session_test.go internal/infra/billingstore/refinement5_1_resumable_session_billing_test.go
```

Result: PASS, exit code 0, no diff.

Artifact hygiene checked all four candidate paths for UTF-8 BOM and trailing spaces/tabs. Result: PASS; no BOM and no trailing whitespace found.

Index and production diff state:

```powershell
git status --short
git diff --cached --name-status
git diff --name-only -- '*.go'
```

Result: the working tree contains exactly the four owned candidate files as untracked paths; the index is empty; the tracked production Go diff inventory is empty. No production Go file was edited, staged, or committed.

Concurrency follow-up: both focused `go test -race` attempts reached the Windows Go toolchain but failed during `runtime/cgo` compilation with `cgo.exe: exit status 2` and no package/test diagnostic. This is an environment/toolchain limitation, not a reported race result; the non-race focused matrix and full requested suite passed.

## 8. Residual risk

The cancellation scenario intentionally has no accepted provider cost evidence, so production provider-cost work remains pending/unreconciled for retry. The tests prove that this diagnostic state and the legitimate customer fixed fee are durable and separated; they do not claim later provider-cost reconciliation succeeds. No broader refinement or Kiro-status completion is claimed by this evidence.
