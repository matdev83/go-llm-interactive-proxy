# Implementation Plan

## 1. Establish experimental configuration and model metadata contracts

- [ ] 1.1 Add failing configuration tests for the default-off native-compaction block
  - Prove omitted configuration resolves to disabled and preserves the existing direct connector behavior.
  - Cover normalization of zero values to reviewed defaults and rejection of negative or over-bound values.
  - Prove enabled native compaction is rejected for the app-server connector kind.
  - Observable completion: configuration tests describe the full supported block and fail against the current implementation.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.7, 9.2, 9.8_
  - _Boundary: config/wiring_
  - _Depends: none_
  - _Validation: go test ./internal/service/... ./internal/codex/..._

- [ ] 1.2 Implement typed native-compaction configuration and lifecycle ownership
  - Add normalized defaults, hard caps, startup validation, effective diagnostics, and runtime close behavior.
  - Keep checkpoint state unconstructed or inactive on the disabled path.
  - Preserve per-connector-instance isolation.
  - Observable completion: the tests from 1.1 pass without changing existing disabled requests.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 8.4, 9.8_
  - _Boundary: config/wiring, backend connector composition root_
  - _Depends: 1.1_
  - _Validation: go test ./internal/service/... ./internal/codex/..._

- [ ] 1.3 (P) Add failing catalog tests for compaction threshold and compatibility metadata
  - Cover `auto_compact_token_limit` and `comp_hash` presence, absence, malformed entries, and fallback catalog behavior.
  - Cover trigger precedence: explicit configuration, catalog value, then derived context-window limit.
  - Observable completion: model-profile tests fail because the current catalog drops the fields.
  - _Requirements: 3.1, 3.2, 3.3_
  - _Boundary: backend connector inventory_
  - _Depends: none_
  - _Validation: go test ./internal/catalog/..._

- [ ] 1.4 Implement connector-private model compaction profiles
  - Preserve catalog metadata without changing root model inventory/public contracts.
  - Validate effective trigger and retained-budget headroom against the resolved hard context window.
  - Keep exact model equality as the initial replay rule.
  - Observable completion: catalog and threshold tests pass for discovered and fallback profiles.
  - _Requirements: 3.1, 3.2, 3.3, 5.1, 9.1_
  - _Boundary: backend connector inventory_
  - _Depends: 1.3_
  - _Validation: go test ./internal/catalog/... ./internal/codex/..._

## 2. Preserve exact OpenAI reasoning and compaction item envelopes

- [ ] 2.1 Add failing exact-item codec and privacy tests
  - Cover valid completed reasoning, valid compaction, missing identity/content, wrong discriminator, invalid JSON, and hard-size limits.
  - Assert errors and logs never contain `encrypted_content` values.
  - Cover exact round-trip stability for the existing OpenAI Responses reasoning dialect.
  - Observable completion: tests demonstrate that current Codex mapping drops or cannot replay required items.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.7_
  - _Boundary: backend connector driven adapter, tests_
  - _Depends: none_
  - _Validation: go test ./internal/codex/..._

- [ ] 2.2 Implement bounded native replay item codecs
  - Add typed compaction trigger and opaque reasoning/compaction replay carriers with strict allowlists.
  - Treat ciphertext as one opaque field and avoid plaintext/token inspection.
  - Ensure malformed or oversized items cannot enter continuation or checkpoint state.
  - Observable completion: codec, round-trip, and privacy tests pass.
  - _Requirements: 2.1, 2.3, 2.4, 2.5, 2.6, 2.7, 4.1, 4.4_
  - _Boundary: backend connector driven adapter_
  - _Depends: 2.1_
  - _Validation: go test ./internal/codex/..._

- [ ] 2.3 Integrate exact reasoning with canonical input/output
  - Emit the existing canonical OpenAI Responses reasoning-part dialect from completed Codex reasoning items.
  - Replay compatible canonical reasoning parts as their original exact envelopes.
  - Reject incompatible dialects before upstream work instead of converting them to text.
  - Observable completion: direct Codex reasoning survives a canonical round trip and remains candidate-bound.
  - _Requirements: 2.1, 2.2, 2.5, 2.6, 9.1, 9.3_
  - _Boundary: backend connector canonical adapter_
  - _Depends: 2.2_
  - _Validation: go test ./internal/codex/... && make parity-checks_

- [ ] 2.4 (P) Fuzz opaque item and event-envelope validation
  - Fuzz arbitrary JSON, discriminator mutation, large encrypted fields, nested content, and malformed output-item events.
  - Assert bounded allocations, no panic, no payload leakage, and stable rejection categories.
  - Observable completion: targeted fuzz runs complete without crash or unbounded growth.
  - _Requirements: 2.4, 2.7, 9.6_
  - _Boundary: tests_
  - _Depends: 2.2_
  - _Validation: go test -fuzz=Fuzz -fuzztime=30s ./internal/codex/..._

## 3. Define compaction planning, estimation, and replacement policy

- [ ] 3.1 Add failing planner tests for safe prefix and live-tail behavior
  - Cover ordinary chat, multi-tool turns, no user boundary, orphan/cross-boundary calls, rollback/edit/fork drift, and a live tail larger than the threshold.
  - Prove the split occurs before the latest user message and preserves every later item exactly.
  - Observable completion: deterministic table tests define bypass, reuse, create, and hard-failure decisions.
  - _Requirements: 3.4, 3.5, 3.6, 3.7, 6.1, 6.2, 6.6_
  - _Boundary: backend connector domain policy, tests_
  - _Depends: 1.4, 2.2_
  - _Validation: go test ./internal/codex/..._

- [ ] 3.2 Implement payload-level effective token estimation
  - Reuse connector-local token/image estimators for instructions, tools, messages, and live suffixes.
  - Model compaction replay cost from provider-reported output tokens or conservative checkpoint metadata, never ciphertext byte tokenization.
  - Return before/after input and serialized-byte measurements for diagnostics.
  - Observable completion: estimator tests distinguish full client history from effective rewritten history.
  - _Requirements: 3.2, 3.3, 3.4, 4.7, 8.2, 8.5, 8.6_
  - _Boundary: backend connector domain policy_
  - _Depends: 1.4, 2.2_
  - _Validation: go test ./internal/codex/..._

- [ ] 3.3 Implement the pure compaction planner
  - Prefer a valid checkpoint, then compare effective input with the trigger, then create only from a safe prefix.
  - Enforce one decision with stable reason categories and no side effects.
  - Skip automatic compaction when no safe benefit exists.
  - Observable completion: all planner tables pass and disabled configuration always returns bypass.
  - _Requirements: 3.4, 3.5, 3.6, 3.7, 3.8, 6.6, 6.7_
  - _Boundary: backend connector domain policy_
  - _Depends: 3.1, 3.2_
  - _Validation: go test ./internal/codex/..._

- [ ] 3.4 Implement retained-message replacement construction
  - Retain eligible recent user-context items newest-first within budget, restore original order, and append exactly one validated compaction item.
  - Keep system instructions outside the replacement and reject unexpected assistant/tool items in a candidate.
  - Observable completion: replacement tests prove ordering, truncation budget, no live-turn duplication, and exactly one compaction item.
  - _Requirements: 4.5, 4.6, 4.7_
  - _Boundary: backend connector domain policy_
  - _Depends: 2.2, 3.2_
  - _Validation: go test ./internal/codex/..._

## 4. Build bounded account- and model-scoped checkpoint state

- [ ] 4.1 Add failing store tests for isolation and committed-candidate semantics
  - Cover session, connector instance, account, model, cache key, client family, comp hash, instructions, and tools scoping.
  - Cover reserve/commit/abort, prior committed checkpoint survival, TTL, LRU, cooldown, close, and defensive-copy behavior.
  - Cover concurrent reservations and managed-account rotation.
  - Observable completion: race-safe store behavior is fully specified before implementation.
  - _Requirements: 3.8, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 5.8, 7.6_
  - _Boundary: backend connector state adapter, tests_
  - _Depends: 1.2_
  - _Validation: go test ./internal/codex/..._

- [ ] 4.2 Implement the native checkpoint store
  - Maintain immutable committed state separately from one active reservation per key.
  - Enforce configured TTL/LRU and code-owned hard caps.
  - Preserve a valid committed checkpoint when a new candidate fails.
  - Observable completion: all store tests pass without exposing keys or payload bodies through diagnostics.
  - _Requirements: 5.1, 5.2, 5.5, 5.6, 5.7, 5.8, 7.6, 8.4_
  - _Boundary: backend connector state adapter_
  - _Depends: 4.1_
  - _Validation: go test ./internal/codex/..._

- [ ] 4.3 (P) Run race coverage for store, cooldown, and shutdown
  - Exercise concurrent get/reserve/commit/abort/invalidate/evict/close operations.
  - Include continuation invalidation calls sharing the same logical lineage.
  - Observable completion: targeted race tests pass repeatedly with no stale commit or data race.
  - _Requirements: 5.5, 5.6, 5.8, 7.5_
  - _Boundary: tests_
  - _Depends: 4.2_
  - _Validation: go test -race ./internal/codex/..._

## 5. Implement strict Responses Compaction V2 execution

- [ ] 5.1 Add failing internal compaction protocol tests
  - Assert the request uses the selected account/model/static shape, appends one trigger, and clears `previous_response_id`.
  - Cover success, missing/multiple compaction items, text output, tool output, malformed events, non-success completion, cancellation, body limits, and provider errors.
  - Assert the internal stream produces no canonical assistant output.
  - Observable completion: deterministic SSE fixtures define the complete accepted protocol.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 4.8, 4.9, 7.5_
  - _Boundary: backend connector driven adapter, tests_
  - _Depends: 2.2_
  - _Validation: go test ./internal/codex/..._

- [ ] 5.2 Implement the V2 internal compaction client and collector
  - Use the existing Codex HTTP/auth/header path with an isolated payload copy and bounded streaming collector.
  - Require exactly one compaction item and successful completion before returning a candidate.
  - Close on cancellation and keep provider error text bounded and ciphertext-safe.
  - Observable completion: protocol tests pass and no legacy compact endpoint is called.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 4.8, 4.9, 7.5_
  - _Boundary: backend connector driven adapter_
  - _Depends: 5.1_
  - _Validation: go test ./internal/codex/..._

- [ ] 5.3 Capture compaction usage and checkpoint evidence
  - Preserve provider-reported input/output totals separately from normal response usage.
  - Carry output token count into checkpoint replay-cost metadata.
  - Mark estimates with existing provenance when provider usage is absent.
  - Observable completion: usage tests prove no double counting and stable provider-vs-estimated authority.
  - _Requirements: 4.7, 8.1, 8.2, 8.6, 8.7_
  - _Boundary: backend connector accounting_
  - _Depends: 5.2, 3.2_
  - _Validation: go test ./internal/codex/..._

## 6. Orchestrate per-account checkpoint creation and exact rewrite

- [ ] 6.1 Add failing coordinator tests for reuse, creation, and fail-open
  - Cover valid checkpoint reuse, source-prefix mismatch, first creation, later re-compaction, in-flight contention, cooldown skip, and hard-limit failure.
  - Prove the full payload snapshot remains available and authoritative.
  - Prove managed account attempts receive independent prepared payloads.
  - Observable completion: coordinator state-machine tests fail against the current request pipeline.
  - _Requirements: 3.8, 5.3, 5.4, 5.6, 5.7, 6.1, 6.2, 6.6, 6.7, 7.1, 7.2, 7.3, 7.4, 7.6, 7.7_
  - _Boundary: backend connector app orchestration, tests_
  - _Depends: 3.3, 3.4, 4.2, 5.3_
  - _Validation: go test ./internal/codex/..._

- [ ] 6.2 Implement exact-prefix rewriting
  - Replace only a verified source prefix, append the untouched suffix, and preserve function-call/output ordering.
  - Reject edits, rollback, fork, model/static-shape drift, or incompatible opaque lineage as optimization misses.
  - Observable completion: rewrite tests pass with byte-stable live-tail fixtures.
  - _Requirements: 5.3, 6.1, 6.2, 6.6, 6.8_
  - _Boundary: backend connector domain policy_
  - _Depends: 3.1, 4.2_
  - _Validation: go test ./internal/codex/..._

- [ ] 6.3 Implement the per-attempt compaction coordinator
  - Run after effective account/model selection and before normal transport opening.
  - Reuse-first, reserve/create/validate/commit, rewrite, and return internal usage.
  - Abort candidates safely, apply classified cooldown, and fail open once only when full history fits.
  - Observable completion: coordinator tests pass for static and managed account preparation.
  - _Requirements: 4.2, 5.4, 5.6, 5.7, 7.1, 7.2, 7.3, 7.5, 7.6, 7.7, 7.8_
  - _Boundary: backend connector app orchestration_
  - _Depends: 6.1, 6.2_
  - _Validation: go test ./internal/codex/..._

- [ ] 6.4 Integrate coordinator with static and managed HTTPS paths
  - Prepare a fresh account-scoped payload for each managed attempt.
  - Preserve existing OAuth refresh, account rotation, downgrade, prompt cache, and stream opening behavior.
  - Decorate the normal stream with internal compaction usage before terminal accounting.
  - Observable completion: deterministic HTTP integration tests show one compaction plus one compacted normal request when enabled and one normal request when disabled.
  - _Requirements: 1.4, 7.2, 7.7, 8.1, 9.2, 9.3, 9.5_
  - _Boundary: backend connector driven adapter_
  - _Depends: 6.3_
  - _Validation: go test ./internal/codex/..._

## 7. Reset and rebuild WebSocket continuation around checkpoints

- [ ] 7.1 Add failing continuation-reset integration tests
  - Prove a newly installed checkpoint clears old `previous_response_id` and invalidates the prior continuation entry.
  - Prove a successful post-checkpoint response creates a new continuation baseline.
  - Cover stale previous-response retry, checkpoint mismatch, and managed account rotation.
  - Observable completion: tests reproduce the invalid mixed-chain behavior before the fix.
  - _Requirements: 6.3, 6.4, 6.5, 6.8, 7.7, 9.3_
  - _Boundary: backend connector WebSocket adapter, tests_
  - _Depends: 6.3_
  - _Validation: go test ./internal/codex/..._

- [ ] 7.2 Integrate checkpoint preparation before WebSocket continuation
  - Apply the coordinator to the full payload before continuation prefix slicing.
  - Skip old continuation when chain reset is required.
  - Ensure retries restore the coordinator-prepared full payload rather than mixing unreduced and compacted histories.
  - Observable completion: WebSocket continuation-reset tests pass for static and managed paths.
  - _Requirements: 6.3, 6.4, 6.5, 6.8, 7.8, 9.3_
  - _Boundary: backend connector WebSocket adapter_
  - _Depends: 7.1_
  - _Validation: go test ./internal/codex/..._

- [ ] 7.3 Expand completed output-item recording for valid new-chain continuation
  - Record exact assistant/reasoning/function output items required to recognize client replay after a normal response.
  - Keep compaction items private to checkpoint state and keep all captured items bounded.
  - Observable completion: ordinary and tool-call post-checkpoint turns can use delta continuation without losing exact reasoning.
  - _Requirements: 2.1, 2.6, 6.5, 8.7_
  - _Boundary: backend connector stream adapter_
  - _Depends: 2.3, 7.2_
  - _Validation: go test ./internal/codex/..._

- [ ] 7.4 Run combined race tests for checkpoint and continuation lifecycles
  - Exercise concurrent normal turns, checkpoint installation, continuation recording, invalidation, cancellation, and connector close.
  - Observable completion: repeated race runs show no duplicate reservation, stale baseline, or unsafe post-close commit.
  - _Requirements: 5.5, 5.6, 5.8, 6.4, 6.5, 7.5, 9.6_
  - _Boundary: tests_
  - _Depends: 7.2, 7.3_
  - _Validation: go test -race ./internal/codex/..._

## 8. Add diagnostics, privacy, and performance evidence

- [ ] 8.1 Add failing accounting and diagnostics tests
  - Cover separate compaction and normal usage, compatibility totals, estimated provenance, metrics outcome categories, and bounded state summaries.
  - Assert raw prompts, account/session IDs, response IDs, item JSON, tool schemas, and ciphertext never appear in logs or diagnostic output.
  - Observable completion: tests define exact privacy and accounting behavior before metrics wiring.
  - _Requirements: 1.5, 2.7, 8.1, 8.2, 8.3, 8.4_
  - _Boundary: backend connector observability, tests_
  - _Depends: 5.3, 6.3_
  - _Validation: go test ./internal/codex/..._

- [ ] 8.2 Implement compaction metrics, diagnostics, and stream usage attachment
  - Emit bounded attempt/hit/miss/failure/cooldown/eviction measurements and before/after token/byte values.
  - Include one-time compaction usage exactly once in provider-billable accounting.
  - Keep labels low-cardinality and content-free.
  - Observable completion: accounting and privacy tests pass and disabled mode produces no compaction measurements.
  - _Requirements: 1.5, 2.7, 8.1, 8.2, 8.3, 8.4, 8.7_
  - _Boundary: backend connector observability_
  - _Depends: 8.1_
  - _Validation: go test ./internal/codex/..._

- [ ] 8.3 Add deterministic long-history performance benchmarks
  - Compare full-history, prompt-cache-only, WebSocket continuation, and checkpoint-reuse request shapes.
  - Include compaction request tokens/latency in break-even reporting.
  - Assert the fixture's reused checkpoint reduces serialized bytes and estimated effective input.
  - Observable completion: benchmark output reports before/after and break-even evidence without claiming universal savings.
  - _Requirements: 8.5, 8.6, 8.7_
  - _Boundary: tests/performance_
  - _Depends: 6.4, 7.3, 8.2_
  - _Validation: go test -bench=NativeCompaction -benchmem ./internal/codex/..._

## 9. Prove compatibility and retain default-off rollout

- [ ] 9.1 Add full disabled-path parity and architecture guard tests
  - Prove no canonical/public/core contract change and no behavior change for app-server or non-Codex backends.
  - Prove disabled direct requests preserve normal payload/event/request-count behavior for HTTP and WebSocket.
  - Observable completion: architecture and parity checks fail if the feature leaks outside the connector or changes default behavior.
  - _Requirements: 1.4, 9.1, 9.2, 9.3, 9.4, 9.5, 9.8_
  - _Boundary: tests/architecture_
  - _Depends: 6.4, 7.3, 8.2_
  - _Validation: make parity-checks && make quality-checks_

- [ ] 9.2 Add environment-gated live Codex compaction smoke coverage
  - Require explicit opt-in credentials/model/environment and use bounded synthetic history.
  - Verify trigger acceptance, exactly one compaction item, successful replay in the next normal turn, retained seeded state, and usage capture.
  - Redact or delete all live artifacts after assertions.
  - Observable completion: the test is skipped by default and produces direct compatibility evidence when explicitly enabled.
  - _Requirements: 9.7_
  - _Boundary: tests/integration_
  - _Depends: 6.4, 7.3, 8.2_
  - _Validation: environment-gated connector live test_

- [ ] 9.3 Execute the connector and repository quality gates
  - Run focused unit/integration tests, race tests, fuzz smoke, parity, and quality checks.
  - Record any environment-only live test as run or explicitly skipped.
  - Confirm disabling the feature removes all checkpoint behavior without migration.
  - Observable completion: all required deterministic gates pass and remaining uncertainty is limited to documented live evidence.
  - _Requirements: 9.4, 9.5, 9.6, 9.8_
  - _Boundary: tests_
  - _Depends: 9.1, 9.2_
  - _Validation: go test ./internal/codex/... ./internal/catalog/... ./internal/service/... && go test -race ./internal/codex/... && make test-unit && make quality-checks && make parity-checks_

- [ ] 9.4 Keep enablement promotion outside this implementation
  - Verify shipped examples/defaults remain disabled and no automatic promotion logic exists.
  - Produce the metrics and live evidence needed for a separate future default-on review.
  - Observable completion: the implementation is fully usable by explicit opt-in and impossible to enable accidentally.
  - _Requirements: 1.1, 1.7, 8.5, 8.6, 9.9_
  - _Boundary: config/wiring, tests_
  - _Depends: 9.3_
  - _Validation: configuration snapshot tests and final diff review_

## Requirements Coverage Check

All acceptance criteria in Requirements 1–9 are mapped to at least one implementation task. No requirement is intentionally deferred inside this specification. Default enablement is explicitly deferred to a separate reviewed change under 1.7 and 9.9.
