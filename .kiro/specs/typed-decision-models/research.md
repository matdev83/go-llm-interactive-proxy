# Research & Design Decisions

## Summary
- **Feature**: `typed-decision-models`
- **Discovery Scope**: Extension (new canonical operation on the existing execution path) with one external protocol family.
- **Baseline**: `main` @ `28b4a02`. Vendor facts re-verified 2026-10-08 (issue #804 research appendix and revision log).
- **Key Findings**:
  - Steering forbids the execution shape proposed in issue #804 §6.2 (a sibling non-streaming `DecisionExecutorView`): `product.md` contract 2, `api-standards.md` procedure step 4 and `routing-and-orchestration.md` ("do not add a second planner/terminal path") all require non-streaming work to be a collection over the canonical event path.
  - The repository already has the exact precedent: `context.compaction` is a protocol-neutral, non-streaming operation that backends serve with one JSON round trip and a `lipapi.NewFixedEventStream`, a dedicated capability (`CapabilityCompaction`) and a typed output event, with pre-output failures classified for failover (`internal/plugins/backends/openresponsescompat/compact.go`).
  - Compatible-provider growth is data-driven: `internal/providerprofiles` families plus one `custom-*-compatible` backend kind per family. TypeSafe, OpenRouter and Command Code all serve the same System One wire at `<base>/systemone`, so one family with three catalog profiles covers them.

## Research Log

### Gap analysis: requirement-to-asset map
| Req | Existing asset | Gap |
| --- | --- | --- |
| 1.1–1.5 | `frontendpipe.Spec` (auth, decode admission, body cap, route-from-body-model, error envelopes, `ClassifyExecute` hook); `internal/stdhttp/contract` route claims | **Missing**: System One decode/encode, 422 `detail[]` errors, ordered `criteria` decoding |
| 1.2 | `encoding/json` tokens (`jsonshape.Preflight` bounds depth and duplicate keys) | **Constraint**: Go maps lose key order; choice `criteria` and score order must be decoded into ordered slices |
| 2.1–2.4 | `lipapi.Event` with typed carriers (`Item`, `Reasoning`); `ValidateEventSequence` | **Missing**: canonical decision request/result types, invariant validation, a result event kind |
| 3.1 | `lipapi.RequiredCapabilities` + `Negotiate` (hard reject before open); per-candidate `Backend.ResolveCaps` | **Missing**: a decision capability; nothing marks a call as a decision |
| 3.2–3.3 | `lipapi.RecoverablePreOutputError` / `IsRecoverablePreOutput`; `OutputCommitted` | **Missing**: decision result must count as committed output; upstream 4xx must stay terminal |
| 3.4 | `providerprofiles` families, `FamilyBinding`, `profileFamilyBuilders`, `wrapCompatibleLifecycle`, `compatmode` config/env keys/static inventory | **Missing**: `systemone-compatible` family, its adapter, three catalog profiles |
| 3.5 | `Call.Validate` authority exclusivity (Items vs Messages) | **Missing**: decision authority exclusive with messages/items/tools; chat-only backends lack the capability |
| 4.1–4.2 | `EventUsageDelta` with `UsagePresence`, `CostNanoUnits`, `CostPresent`, `CostSource` | None: existing usage evidence carries tokens and provider-reported cost |
| 4.3 | `checkCheapCredit` → `authorizeBillingOnce`; one `BillingCallID` per `Execute`; quote size from `modelcatalog.DefaultSizeEstimator` | **Gap**: estimator counts message/tool bytes only, so a decision quotes as size 0 |
| 4.4 | stock `lipstd` installs no billing ports | None |
| 5.1 | `runSecretGuardStage` hands the whole `Call` to guards; the standard guard scans `Instructions`/`Messages`/`Items` fragments only | **Gap**: decision evidence would bypass the guard (silent egress) |
| 5.2 | `diag` structured logging conventions; no content logging by default | Adapter must keep that posture |
| 5.3 | frontends build calls from decoded fields only | Adapter must not copy client headers upstream |

### Execute-path behaviour with a message-free call
- `prepareRequest` calls `call.Validate()` first, then identity/secure session, local-turn handlers, secret guard, session classification, tool-catalog filters, request transforms (re-validated afterwards by `RunRequestTransformStage`), pre-request handlers, credit screen, route plan, exposure admission, attempt open, stream assembly.
- Message iterators tolerate empty slices. Local-turn handlers match on ingress text and do not claim an empty call. Remote session classification uses a closed operation vocabulary, so an unknown operation yields no egress.
- A request transform that injects instructions into a decision call fails the post-transform `Validate` (fail closed, explicit error), because decision authority is exclusive.
- `executor_open_attempt.go` negotiates `RequiredCapabilities(call)` against `ResolveCaps` per candidate before opening; a missing hard capability rejects before upstream I/O.

### Executable connectors
- `pkg/lipsdk/backendplugin` converts `lipapi.Call` to protobuf frames and has no decision field. The existing `openrouter`, `commandcode-*`, `cloudflare` and `databricks` connectors are out-of-process. V1 must keep decision calls away from them: they never declare the decision capability, and the bridge rejects a decision call explicitly as defence in depth.

### External protocol (System One)
- `POST {base}/systemone`, `Authorization: Bearer`, body `{model, state, questions}`; `state` string/object/array; `questions` ID-keyed; `noul` (`criteria` optional `{true,false}`), `choice` (`criteria` object, ≤255 options, `null` descriptions allowed), `score` (`criteria` ordered array, 2–10 levels in prose; OpenAPI says `minItems: 1`).
- Response `{model, answers, usage{input_tokens, output_tokens}}`; `noul` has no `confidence`; choice `probabilities` map over every option; score `score`, `legend`, `probabilities` keyed by zero-based string index, `confidence`.
- Errors 401, 422 (`{"detail":[{"loc","msg","type"}]}`), 429, 529. OpenRouter adds 402, 413, 524 and response fields `id`, `provider`, `usage.cost` (USD); Command Code adds 403 `upgrade_required`.
- Base URLs whose default path is `/systemone`: `https://api.typesafe.ai/v1`, `https://openrouter.ai/api/v1`, `https://api.commandcode.ai/provider/v1`.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
| --- | --- | --- | --- | --- |
| A: sibling executor (issue #804 §6.2) | `DecisionExecutorView`, `EvaluateDecision` facade, `Backend.EvaluateDecision`, attempt/terminal logic extracted from the stream executor | No canonical-stream change | Violates three steering rules; needs a new SDK seam; extraction from `ExecuteLargeBody`/`openWireAttemptTx` (≈750 intertwined lines) does not fit one PR; second terminal path | Rejected |
| B: canonical operation on the event path | Typed `Call.Decision`, operation `decision.evaluate`, capability `decisions`, one `EventDecisionResult`; backends return a fixed event stream | Reuses routing, failover, commitment, usage, billing, terminal ownership unchanged; follows the compaction precedent; fits budget | Touches `pkg/lipapi` (revalidation trigger); message-oriented stages must tolerate empty calls (verified above) | **Selected** |
| C: decision as chat prompt / JSON text | Encode questions into messages, parse model text | No contract change | Fabricates distributions; issue non-goal | Rejected |
| D: opaque `Call.Extensions` JSON | Carry vendor JSON untyped | Small | Untyped, unvalidated, invisible to negotiation; explicitly excluded by the issue | Rejected |

## Design Decisions

### Decision: canonical operation instead of a sibling executor
- **Context**: Requirements 2–4 need routing, failover, usage and billing exactly as for inference.
- **Selected Approach**: Option B. The decision result is one typed canonical event; non-streaming delivery is collection over the canonical stream.
- **Rationale**: Steering compliance and reuse of every authority without extraction.
- **Trade-offs**: `pkg/lipapi` grows a small typed surface; the issue body's §6.2 shape should be updated to match.

### Decision: protocol-neutral operation name
- **Selected Approach**: `lipapi.OperationDecisionEvaluate = "decision.evaluate"`, mirroring `context.compaction`. A later OpenAI Decisions frontend produces the same operation; its refusal and boolean-value semantics extend the result type then.

### Decision: decision result commits output
- **Selected Approach**: `OutputCommitted` returns true for `EventDecisionResult`. Invalid upstream answers are rejected inside the adapter before the event is emitted, so they remain pre-output and recoverable (Requirement 2.2, 3.2).

### Decision: secret guard fails closed
- **Selected Approach**: when the secret-guard plane has guards, the executor rejects a decision call with a policy-denied error before the guard stage. Extending guard fragments to decision evidence is deferred.
- **Rationale**: avoids a silent unguarded egress path at minimal cost.

### Decision: one compatible family, static model inventory
- **Selected Approach**: `systemone-compatible` family; catalog profiles use `discovery: static` because only TypeSafe and Kev expose a Jev-shaped model list and OpenRouter's System One path has none.
- **Follow-up**: TypeSafe-shaped discovery is deferred with the `GET /v1/models` collision work.

### Synthesis
- **Generalization**: the canonical types are vendor-neutral (positionally ordered questions with optional IDs), so the deferred OpenAI Decisions frontend reuses them without a second model.
- **Build vs adopt**: no Go System One SDK exists; the adapter is ~300 lines of stdlib JSON. The session-classification `jev_wire.go` stays isolated and is not imported.
- **Simplification**: no new SDK seam, no new execution class, no new metering unit, no connector ABI change; per-profile limits beyond the System One baseline are deferred.

## Risks & Mitigations
- A feature transform mutates a decision call — post-transform `Validate` fails closed with an explicit error.
- An executable connector declares the decision capability — the call bridge rejects decision calls explicitly.
- Aggregator limits are smaller than TypeSafe's (OpenRouter Jev 32K) — upstream 422 is surfaced as 422; per-profile limits deferred.
- Budget pressure (≈1,350 non-test Go lines planned) — frontend and backend share only `pkg/lipapi`; no large-body path or extension fields in V1.

## References
- Issue #804 body, research appendix and revision log.
- [TypeSafe System One API](https://docs.typesafe.ai/api), [models](https://docs.typesafe.ai/models), [OpenAPI](https://api.typesafe.ai/openapi.json)
- [OpenRouter Jev guide](https://openrouter.ai/docs/guides/community/jev), [Command Code provider API](https://commandcode.ai/docs/provider)
- `.kiro/steering/product.md`, `api-standards.md`, `routing-and-orchestration.md`, `delivery.md`; `docs/adr/0008-hybrid-backend-connector-plugins.md`
