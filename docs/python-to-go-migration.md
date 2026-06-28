# Python LIP to Go proxy: routing and capability migration notes

This document summarizes semantic differences operators should expect when moving from the Python `llm-interactive-proxy` to the Go `go-llm-interactive-proxy` core.

## Routing

- Selectors use the same string-oriented failover syntax (`a:model|b:model`, weighted branches, `[first]` steering). `routing.max_attempts` caps **B-leg opens** (initial open plus recv-phase replacements) per logical request.
- Model-only selectors require an explicit `default_route` / default backend resolution path; unresolved model-only selectors fail deterministically instead of surfacing as unknown-backend surprises at open time.
- Route-wide stickiness is opt-in with global selector params, for example `{affinity=session}[weight=1]a:model^[weight=1]b:model` or `{affinity=client}...` (aliases: `{session_sticky}`, `{client_sticky}`). Sticky bindings target backend instance ids and are revalidated on every request; unhealthy, absent, or context-ineligible backends are cleared and re-selected from currently eligible candidates.

## Interleaved thinking (`[thinker]`)

Go LIP ports Python-era interleaved thinking as core-owned routing and runtime behavior. The feature is **disabled by default**; enable it under `interleaved.enabled` in runtime config. Selectors without `[thinker]` keep existing routing behavior unchanged (`SB-ROUTE-regress-non-thinker-noninterference`).

### Selector forms

| Form | Behavior |
| --- | --- |
| `[thinker]` on one weighted branch | Accepted; marks that branch as the thinker arm (`SB-ROUTE-parse-thinker-forms`). |
| `[thinker=1]`, `[thinker=yes]`, `[thinker=true]` | Accepted as true-valued thinker annotations. |
| `[thinker=0]`, `[thinker=no]`, `[thinker=false]`, empty/unrecognized values | Rejected at parse time. |
| `[first]` plus `[thinker]` on the same branch | Rejected at parse time. |
| More than one thinker branch | Rejected at parse time. |
| One thinker branch plus one non-thinker branch whose target is a parallel executor group | Accepted hybrid form only; general weighted/parallel mixing stays rejected (`SB-ROUTE-parse-thinker-hybrid`). |

Thinker-aware weighted cycles repeat non-thinker branches by effective weight, append the thinker branch once, persist the cursor on the A-leg, reset stale selector state, honor `[first]` before cycle advancement, and suppress the thinker arm on continuation turns (`SB-ROUTE-planner-thinker-cycle`, `SB-ROUTE-planner-thinker-suppression`).

### Visibility modes

- **Hidden** (default `interleaved.stream_to_client: hidden`): thinker output is captured but not surfaced; the same logical request continues to an executor branch (`SB-ORCH-interleaved-hidden-continuation`).
- **Visible** (`interleaved.stream_to_client: visible`): sanitized thinker reasoning deltas are surfaced before executor output; memo wrapper tags are stripped from client-visible content (`SB-ORCH-interleaved-visible-continuation`).

After client-visible thinker output begins, Go LIP preserves the no-retry-after-output guarantee (no silent failover or restart).

**Visible thinker pipeline (Go behavior):** client-visible thinker reasoning does **not** route through the normal executor response-part hook, BTP, or PTC path. `interleavedContinuationStream` synthesizes sanitized canonical `EventReasoningDelta` events from the captured thinker continuation, records output-commit accounting locally (`recordVisibleOutput`), and emits them before executor output. Executor output in the same logical request still uses the standard attempt stream path (response hooks, traffic observers, completion gates). Response-part plugins cannot mutate visible thinker deltas in the current Go implementation.

### Memo and continuation behavior

- Thinker turns receive configured instructions and have tools suppressed before backend open.
- Memo content is captured from canonical events (including streaming), bounded by `interleaved.max_memo_bytes` (default 16 KiB), and stored on the A-leg with a memo reference (`SB-CONT-interleaved-state-round-trip`).
- Executor turns inject the stored memo as planning context when budget remains (`interleaved.regular_turns_remaining`, default 2); injection decrements the budget, skips expired memos, and avoids duplicate equivalent content.
- When a thinker branch is selected, the runtime opens a thinker B-leg, stores memo state, suppresses thinker selection for the continuation executor B-leg in the same A-leg, and records both attempts in lineage.
- Hybrid selectors run either the thinker continuation into the embedded parallel group (`SB-ORCH-interleaved-hybrid-thinker-continuation`) or the existing parallel race unchanged when the parallel executor arm is chosen (`SB-ORCH-interleaved-hybrid-parallel-race`).

**Memo durability (Go behavior):** durable continuity stores (SQLite and bun-backed) round-trip interleaved **cycle state and memo reference** on the A-leg (`SB-CONT-interleaved-state-round-trip`); they do **not** store memo bodies. The default `runtimebundle` wires `MemoStore` as process-local in-memory (`interleavedthinking.NewMemoStore` in `internal/infra/runtimebundle/interleaved.go`). Memo bodies are therefore lost on process restart unless a future durable `MemoStore` implementation is supplied. After restart, a persisted memo ref with no matching in-memory body skips injection (`MemoOutcomeSkippedMissing`) per existing shaping behavior; cycle state still round-trips.

### Python differences

| Concern | Python LIP | Go LIP |
| --- | --- | --- |
| Default | Feature present when configured in Python deployments | Disabled unless `interleaved.enabled: true` |
| Implementation surface | Controller/runtime mix | Core routing, runtime, and B2BUA continuity only; no interleaved feature plugin in v1 |
| Memo extraction | Provider/controller specific paths | Canonical event stream only (`internal/core/interleavedthinking`) |
| Visible output | Protocol-specific surfacing | Canonical reasoning deltas; frontends must encode legally or fail deterministically |
| Hybrid selectors | One thinker plus one parallel executor expression | Same narrow hybrid exception; other weighted/parallel mixing rejected |
| Diagnostics | Varies by deployment | Bounded attributes only; memo body and prompt text excluded from high-cardinality logs |

Scenario IDs above map to executable evidence in [spec-bundle-routing-scenarios.md](spec-bundle-routing-scenarios.md), [spec-bundle-orchestration-scenarios.md](spec-bundle-orchestration-scenarios.md), and [spec-bundle-continuity-scenarios.md](spec-bundle-continuity-scenarios.md).

## Capabilities

- Capability negotiation is **candidate-aware** for bundled backends: required features are checked against the resolved backend/model pair before upstream I/O. Rejects happen before streaming starts; downgrades are explicit and attempt-local relative to the immutable client baseline.

### Capability catalog surface (Python vs Go)

| Concern | Python `llm-interactive-proxy` | Go `go-llm-interactive-proxy` |
| --- | --- | --- |
| Where catalogs live | Connector/strategy modules and model services (scattered per provider) | Bundled backends: OpenAI-hosted narrowing in [`internal/plugins/backends/openaicaps`](../internal/plugins/backends/openaicaps); other providers in `internal/plugins/backends/<id>` via `ResolveCaps` |
| Negotiation timing | Varies by transport layer and controller | Single `lipapi.Negotiate` path in the executor **before** `Backend.Open` for each attempt |
| Downgrade vs reject | Policy depends on connector and middleware | Explicit `NegotiationDowngrade` applies attempt-local changes; hard missing caps surface as `ErrCapabilityReject` |
| Operator drift checks | Pytest + connector unit tests | Go tests on catalogs (e.g. `caps_catalog_test.go`) plus executor routing tests |

## Continuity

- In-memory and SQLite stores share the same composition entry (`pluginreg.OpenContinuityStore`). SQLite persists A-leg and attempt lineage across process restarts when configured.

## Diagnostics

- Optional JSON plugin inventory is available at the configured `diagnostics.inventory_path` (for example `/debug/inventory`) when diagnostics are enabled.

For protocol-level differences, prefer conformance tests under `internal/testkit/conformance` and golden fixtures under `testdata/`.
