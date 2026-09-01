# Implementation Plan

Implementation is TDD-first and must finish generic downstream cache-affinity hinting in this single workstream. Do not create a follow-up feature/spec for missing provider projection, precedence, observability, or generic connector support described here.

The executing model should treat the design as authoritative and avoid provider research beyond verifying exact current code locations and current documented field names/limits already cited in `research.md`. When a task says "reuse", first locate the existing implementation and extend it; do not create a parallel subsystem.

## 1. Freeze Identity, Precedence, and Compatibility Contracts With RED Tests

- [ ] 1.1 Add RED tests for effective-hint precedence
  - Create a focused table-driven contract covering: explicit provider-specific client hint; explicit protocol-neutral `PromptCacheKey`; proxy-generated fallback; unsupported/no hint.
  - Pin the precedence order exactly: explicit provider-specific > explicit protocol-neutral cache key > generated fallback > none.
  - Pin conflicting explicit carriers to deterministic documented precedence or validation failure; never rely on header/map iteration order.
  - Pin that a generated fallback never overwrites a non-empty explicit client key.
  - Pin that unsupported provider capability returns no newly synthesized hint.
  - _Requirements: 1.1-1.3, 4.1-4.6, 5.1-5.5_
  - _Boundary: protocol-neutral effective-hint resolver only_
  - _Depends: none_
  - _Validation: focused unit tests, initially RED_

- [ ] 1.2 (P) Add RED tests for trusted conversation scope and privacy-safe derivation
  - Prove proxy-owned `AuthoritativeSessionID` is the preferred generic scope.
  - Prove arbitrary `SafeMetadata`, provider headers, principal/user ID, IP, request ID, and resume token do not become generic trusted scope.
  - Prove the same trusted session produces the same fallback value; distinct sessions produce distinct values.
  - Prove the generated value does not contain the raw session ID and is restricted to transport-safe bounded characters/length.
  - Prove configured key/domain changes alter only the generated optimization identifier and not session/authorization state.
  - Prove no trusted scope yields no generated hint under ignore policy and the existing fail-closed identity policy remains fail-closed where applicable.
  - _Requirements: 2.1-2.6, 3.1-3.7, 7.1-7.5_
  - _Boundary: trusted scope adapter + opaque derivation only_
  - _Depends: none_
  - _Validation: deterministic derivation/session tests, initially RED_

- [ ] 1.3 (P) Add RED capability/profile tests
  - Characterize the existing provider/model/API-flavor capability/profile mechanism that will host downstream affinity support.
  - Pin explicit fields for supported/unsupported, semantic, transport, provider wire name, max length, synthesis permission, and explicit-client preservation (or equivalent repository-native representation).
  - Pin unknown OpenAI-compatible providers to synthesis disabled by default.
  - Pin direct Anthropic and Gemini generic downstream session-hint synthesis disabled.
  - Pin capability differences by API flavor where xAI/OpenAI/Mistral paths differ.
  - Pin invalid enabled capabilities (empty wire name, invalid transport, impossible max length) to startup/profile validation failure.
  - _Requirements: 5.1-5.7, 6.1-6.8, 10.4-10.6_
  - _Boundary: compiled provider/backend capability metadata_
  - _Depends: none_
  - _Validation: provider/profile compile and validation tests, initially RED_

- [ ] 1.4 (P) Add RED architecture/compatibility tests
  - Fail if generic core switches on provider names or literal provider wire carriers (`x-grok-conv-id`, `x-session-id`, `x-session-affinity`, provider-specific `session_id` mapping).
  - Fail if generated hints are persisted as a new durable store/table or require hot-path database/network lookup.
  - Fail if raw hints/session IDs appear as bounded-cardinality metric labels or ordinary cache-affinity logs.
  - Pin backend-plugin old-peer/feature negotiation compatibility and prove the current `proxy_owned_session_id` plus prompt-cache semantic carrier remain sufficient unless implementation demonstrates otherwise.
  - Pin that residency target/generation IDs remain independent from downstream affinity values.
  - _Requirements: 1.1-1.6, 3.2-3.7, 7.1-7.6, 10.1-10.5_
  - _Boundary: architecture and executable-backend compatibility guards_
  - _Depends: none_
  - _Validation: archtest/backendplugin contract tests, initially RED_

## 2. Implement the Minimal Provider-Neutral Effective-Hint Layer

- [ ] 2.1 Implement the protocol-neutral hint/source/capability contract
  - Place the new types in the narrowest existing SDK/core package that already owns provider-neutral request metadata/capability semantics; do not add provider wire names to `pkg/lipapi` canonical trajectory fields.
  - Represent only one logical semantic initially: conversation/cache routing affinity.
  - Represent source classification (`none`, explicit provider, explicit prompt-cache, proxy-generated) for tests/telemetry.
  - Add strict bounds and validation; unknown enum/transport values fail closed.
  - Keep the hint value opaque; generic code must not parse provider content from it.
  - _Requirements: 1.1-1.6, 4.1-4.6, 5.1-5.7_
  - _Boundary: provider-neutral downstream-affinity contract_
  - _Depends: 1.1, 1.3_
  - _Design: Domain Model; Provider capability_

- [ ] 2.2 Implement secure deterministic fallback derivation
  - Reuse an existing secure keyed-derivation facility if one already satisfies the design; otherwise add the smallest configuration/key component required.
  - Use explicit domain separation for downstream cache affinity v1.
  - Prefer HMAC-SHA256 or an existing equivalent keyed primitive; encode as bounded URL/header-safe ASCII.
  - Choose one stable output length that fits the strictest supported initial provider limit after verifying current limits in provider docs/code.
  - Do not dynamically truncate the same generic hint differently in multiple layers; provider adapters may reject an impossible capability limit rather than silently weaken identity.
  - Never include raw session/principal/resume-token text in the emitted value.
  - Add startup validation for synthesis enabled without usable keying material.
  - _Requirements: 2.1-2.6, 3.1-3.7, 7.1-7.5_
  - _Boundary: stateless derivation/configuration component_
  - _Depends: 1.2, 2.1_
  - _Design: Opaque Fallback Derivation_

- [ ] 2.3 Integrate trusted conversation-scope resolution with existing session/affinity views
  - Reuse the already-admitted `session.SessionView`/execution views; do not read secure-session storage again in the request hot path.
  - Prefer `AuthoritativeSessionID` and obey existing missing-identity policy.
  - Do not elevate `ClientSessionHint` in paths where current secure affinity deliberately clears/rejects it.
  - Do not create a second affinity binding/store or session registry.
  - Add characterization tests around session resume/ordinary turns and distinct sessions.
  - _Requirements: 1.4-1.6, 2.1-2.6, 7.1-7.5_
  - _Boundary: existing execution/session view integration_
  - _Depends: 2.2_
  - _Design: Conversation Scope Adapter_

- [ ] 2.4 Implement the effective-hint resolver at the selected-backend boundary
  - Run resolution only after a concrete backend/model/API flavor capability is known and before provider wire encoding.
  - Preserve exact precedence from task 1.1.
  - Reuse existing `PromptCacheKey`/semantic-extension authority; do not duplicate it in unrelated metadata maps.
  - Return no hint for unsupported/disabled capability or missing trusted fallback scope.
  - Keep the resolver side-effect-free apart from bounded telemetry classification; no DB/network/goroutine/session-map work.
  - Ensure retry/failover/race attempts can resolve independently for each selected backend capability while sharing the same logical trusted conversation scope.
  - _Requirements: 1.1-1.6, 4.1-4.6, 5.1-5.7, 7.1-7.5, 8.1-8.6_
  - _Boundary: attempt preparation after routing, before backend wire construction_
  - _Depends: 2.1, 2.3_
  - _Design: Hint Resolution; Request Flow_

## 3. Add Provider/Model Capability and Exact Wire Projection

- [ ] 3.1 Extend the existing provider/profile compiler with downstream-affinity capability
  - Locate the current provider/model profile/capability structures used by built-in and executable backends; extend them rather than creating a second provider catalog.
  - Support semantic, transport, wire name, maximum length, synthesis enablement, and explicit-client preservation (or equivalent normalized representation).
  - Compile immutable/generation-local capability data so hot-path lookup is O(1) and allocation-light.
  - Add an explicit operator override/disable using current configuration patterns.
  - Unknown provider/profile remains disabled unless intentionally declared.
  - _Requirements: 5.1-5.7, 7.1-7.5, 10.4-10.6_
  - _Boundary: provider/backend configuration and capability compilation_
  - _Depends: 1.3, 2.1_
  - _Design: Provider/Model Capability; Configuration_

- [ ] 3.2 Implement OpenAI Responses projection
  - Locate the existing OpenAI/OpenResponses request preparation path and its current `PromptCacheKey` forwarding.
  - Feed the resolved effective hint into JSON `prompt_cache_key` only when the selected capability says synthesis/projection is supported.
  - Preserve a non-empty explicit client `prompt_cache_key` exactly; generated fallback fills absence only.
  - Do not add this field to generic unknown OpenAI-compatible requests.
  - Add exact-body fixture tests covering explicit, generated, disabled, unsupported, and max-length cases.
  - _Requirements: 4.1-4.6, 5.1-5.7, 6.1, 6.8, 8.1-8.6_
  - _Boundary: OpenAI Responses backend/provider preparation only_
  - _Depends: 2.4, 3.1_
  - _Validation: local request-wire fixture tests_

- [ ] 3.3 (P) Implement xAI Chat Completions and Responses projection
  - Chat Completions: project to `x-grok-conv-id` header for the explicitly supported xAI capability.
  - Responses: project to `prompt_cache_key` for the explicitly supported xAI Responses capability.
  - Preserve explicit client carrier values and validate syntax/length without logging the value.
  - Keep Chat vs Responses capability distinct; do not infer one from protocol compatibility alone.
  - Add exact header/body fixture tests for both API paths.
  - _Requirements: 4.1-4.6, 5.1-5.7, 6.2-6.3, 8.1-8.6_
  - _Boundary: xAI backend/provider preparation only_
  - _Depends: 2.4, 3.1_
  - _Validation: local request-wire fixture tests_

- [ ] 3.4 (P) Implement Mistral projection
  - Locate supported Mistral Chat/FIM/provider paths and project the effective hint to documented `prompt_cache_key` only on those capabilities.
  - Preserve explicit client values and no-op for unsupported API flavors.
  - Add exact-body fixture tests and one regression proving a generic OpenAI-compatible Mistral-like custom endpoint without declared capability receives no field.
  - _Requirements: 4.1-4.6, 5.1-5.7, 6.4, 6.8_
  - _Boundary: Mistral backend/provider preparation only_
  - _Depends: 2.4, 3.1_
  - _Validation: local request-wire fixture tests_

- [ ] 3.5 (P) Implement OpenRouter sticky-session projection
  - Use the existing OpenRouter backend/provider adapter and choose one canonical wire carrier per path: JSON `session_id` or `x-session-id`; do not emit both unless current OpenRouter adapter contract already requires it.
  - Preserve any explicit client OpenRouter session carrier before generated fallback.
  - Keep the affinity advisory: existing provider ordering/failover/routing features retain authority.
  - Do not convert `session_id` into Go-LIP continuation/session authority.
  - Add exact wire tests and a regression proving different Go-LIP sessions produce different opaque OpenRouter values.
  - _Requirements: 1.1-1.6, 4.1-4.6, 5.1-5.7, 6.5, 8.1-8.6_
  - _Boundary: OpenRouter provider/broker projection only_
  - _Depends: 2.4, 3.1_
  - _Validation: local OpenRouter request fixtures_

- [ ] 3.6 (P) Complete explicitly profiled Fireworks/RunInfra-compatible projection and negative providers
  - Reuse the generic capability/profile declaration path for known Fireworks/RunInfra-compatible endpoints; do not special-case service names in core.
  - Support only declared documented carriers (`x-session-affinity`, `prompt_cache_key`, or profile-selected equivalent); never broadcast multiple speculative hints.
  - Add negative tests proving direct Anthropic and direct Gemini receive no synthesized generic affinity carrier.
  - Add a generic unknown OpenAI-compatible negative test proving no new field/header appears.
  - _Requirements: 5.1-5.7, 6.6-6.8_
  - _Boundary: provider-profile extension plus negative compatibility coverage_
  - _Depends: 2.4, 3.1_
  - _Validation: profile compile + request-wire fixtures_

## 4. Keep Executable Connectors and Existing Cache Subsystems Aligned

- [ ] 4.1 Reuse current backend-plugin carriers without redundant ABI growth
  - Trace the host-to-connector path for `proxy_owned_session_id`, `prompt_cache_key`, and semantic extensions.
  - Implement connector-side effective-hint resolution/projection using existing negotiated information where sufficient.
  - Do not derive trusted session authority from `SafeMetadata` or arbitrary provider headers.
  - Prove old peers and connectors without capability remain ordinary inference-only peers and receive no generated hint.
  - If host-only derivation key material cannot safely be shared, carry only the already-derived opaque fallback through the existing semantic-extension mechanism. Add a new protocol minor/feature only if the current extension carrier cannot represent it; document the proof in code/PR if required.
  - Do not add a redundant top-level protobuf session/cache identity field.
  - _Requirements: 10.1-10.5, 11.1-11.5_
  - _Boundary: executable backend-plugin/connector bridge_
  - _Depends: 2.4, 3.1_
  - _Validation: backendplugin negotiation/conversion/TCK tests_

- [ ] 4.2 Align prompt-cache residency/control integration without changing authority
  - Add regression tests proving generated downstream hint values never become `TargetID`, `GenerationID`, control handle, expiry, or renewal identity in the existing residency subsystem.
  - Ensure residency observations continue to use effective backend/provider preparation and provider evidence.
  - Ensure keep-warm scheduling consumes only existing residency observations/handles and is not armed merely because a downstream hint was emitted.
  - Preserve provider/account/region/downstream affinity checks in maintenance control.
  - _Requirements: 1.1-1.6, 5.7, 9.2-9.3, 11.2-11.3_
  - _Boundary: integration regression only; no scheduler redesign_
  - _Depends: 2.4, provider projection tasks_
  - _Validation: focused prompt-cache residency + keep-warm regression tests_

- [ ] 4.3 Align routing affinity and continuation tests
  - Prove internal route affinity binding continues to choose backend candidates independently of the downstream provider hint.
  - Prove failover/races may reuse the same logical hint across capable arms without reusing provider cache residency or continuation authority.
  - Prove `previous_response_id`, Codex turn-state tokens, WebSocket continuation state, and transport connection identifiers remain separately scoped.
  - Add a regression specifically rejecting reuse of a per-turn sticky token as a cross-turn generated hint.
  - _Requirements: 1.4-1.6, 8.1-8.6_
  - _Boundary: runtime routing/continuation characterization only_
  - _Depends: 2.4, 4.1_
  - _Validation: focused runtime/continuation/routing tests_

## 5. Add Truthful Observability, Documentation, and Reusable TCK Coverage

- [ ] 5.1 Add bounded hint-source/projection observability
  - Reuse existing metrics/telemetry patterns and dimensions.
  - Emit low-cardinality source classification: none, explicit provider, explicit prompt-cache, proxy-generated.
  - Emit projection classification: applied, unsupported, disabled, invalid (adjust names to existing metric conventions).
  - Never include actual hint values, raw `PromptCacheKey`, session IDs, or residency target IDs as labels.
  - Do not emit `cache_hit=true` or equivalent from hint projection alone.
  - Correlate with existing cached-token/cache-read/cache-write evidence at safe backend/provider/model-class dimensions where available.
  - _Requirements: 7.6, 9.1-9.6_
  - _Boundary: observability only_
  - _Depends: 2.4, provider projection tasks_
  - _Validation: telemetry unit tests + cardinality/privacy assertions_

- [ ] 5.2 Build a reusable downstream-affinity TCK
  - Certify effective precedence, secure generated fallback, capability-disabled behavior, explicit preservation, exact provider projection hooks, legacy connector behavior, and privacy constraints independently of frontend protocol.
  - Provide adapter hooks/fixtures so new providers can prove the generic capability contract without frontend × backend Cartesian tests.
  - Include negative unknown-compatible, Anthropic, and Gemini cases.
  - Include failover/race advisory semantics and residency-identity separation.
  - _Requirements: 4.1-4.6, 5.1-5.7, 6.1-6.8, 8.1-8.6, 10.5_
  - _Boundary: reusable testkit/contract layer_
  - _Depends: 3.2-3.6, 4.1-4.3_
  - _Validation: reference backend/connector TCK passes_

- [ ] 5.3 Update active operator/developer documentation
  - Document the four-layer distinction: Go-LIP route affinity; downstream cache-affinity hinting; prompt-cache residency/control; keep-warm orchestration.
  - Document precedence and why explicit client/harness hints beat generated fallback.
  - Document supported initial provider/API matrix and explicitly unsupported generic cases.
  - Document that cache hint projection is advisory and cache usage/residency evidence is the truth source.
  - Document configuration/secret/key rotation behavior and that rotation may reduce cache hits without affecting correctness.
  - Update comments/docs that currently risk conflating session, prompt-cache-key, and residency identity. Do not rewrite archived completed specs as implementation history unless adding a clearly marked forward-reference is necessary.
  - _Requirements: 10.6, 11.2-11.6_
  - _Boundary: active docs/comments/config examples only_
  - _Depends: 3.1-3.6, 4.2_
  - _Validation: docs/config examples match implemented field names/defaults_

- [ ] 5.4 Add opt-in empirical cache-effect validation
  - Add or document credential-gated live tests for at least one provider with observable cached-token/cache-hit evidence (prefer an already-supported direct provider test harness).
  - Send an agent-like stable prefix (instructions + tool schema) over multiple turns with generated affinity enabled and disabled while keeping prompt semantics identical.
  - Record/compare provider-reported cache evidence; do not make live cache improvement a deterministic CI assertion because provider load/eviction is nondeterministic.
  - CI/default quality gates must skip cleanly without credentials.
  - _Requirements: 9.2-9.5_
  - _Boundary: opt-in integration evidence only_
  - _Depends: at least one provider projection task, 5.1_
  - _Validation: credential-gated live test/manual evidence path_

## 6. Performance, Architecture, and Completion Gates

- [ ] 6.1 Benchmark and ratchet hot-path overhead
  - Add focused benchmarks for: explicit hint path; generated fallback path; unsupported/no-hint path.
  - Confirm no DB/network calls, goroutine creation, full-prompt hashing, or unbounded map growth.
  - Confirm capability lookup uses compiled/generation-local state.
  - Keep allocations bounded; optimize/pool only if benchmark evidence shows a material regression.
  - Add a regression/architecture test if the repository has an existing hot-path rule framework suitable for this feature.
  - _Requirements: 7.1-7.6, 11.6_
  - _Boundary: performance verification and minimal fixes_
  - _Depends: 2.4, 3.1_
  - _Validation: focused `go test -bench`/allocation checks; no speculative micro-optimization_

- [ ] 6.2 Run compatibility, race/leak, and repository quality gates
  - Run focused downstream-affinity, provider wire, routing/continuation, residency, keep-warm, backendplugin, and TCK suites.
  - Run applicable race/goleak tests around shared immutable capability/derivation state; the feature should not add new goroutine lifecycle surfaces.
  - Run repository default unit tests and quality/architecture checks using the current documented commands (`make quality-checks`, unit-test target, architecture checks, and change-size check where applicable).
  - Confirm no provider credentials are required for normal CI.
  - Confirm the branch remains spec implementation scope only when executing from this plan; unrelated refactors are excluded.
  - _Requirements: 10.1-10.6, 11.1-11.6_
  - _Boundary: verification only_
  - _Depends: 5.1-5.4, 6.1_
  - _Validation: full repository gates_

- [ ] 6.3 Perform the no-follow-up completion review
  - Verify the generic resolver, secure derivation, provider/model capability, exact provider projection, connector parity, observability, documentation, TCK, and initial provider matrix are all implemented.
  - Search production code/spec/docs for downstream cache-affinity TODOs/placeholders and remove or resolve any that are prerequisites of this feature.
  - Verify no central provider-name switch or universal OpenAI-compatible injection exists.
  - Verify direct Anthropic/Gemini and unknown-compatible negative behavior remains explicit.
  - Verify the completed residency/keep-warm designs still own residency/renewal and were not partially duplicated.
  - Confirm future provider support is a normal profile/adapter addition against this completed contract and does not require a new generic cache-hint architecture.
  - Archive this spec only after all implementation tasks and validation pass according to the repository's normal completion workflow.
  - _Requirements: 11.1-11.6_
  - _Boundary: final whole-feature review_
  - _Depends: 6.2_
  - _Validation: final requirements/design/task traceability + repository search for unresolved generic scope_

## Task Graph and Parallelization Summary

- `1.1`-`1.4` can run in parallel after codebase reconnaissance.
- `2.1` is the shared contract root; `2.2` and capability work can overlap once its shapes are stable.
- `3.2`-`3.6` are provider-specific and intentionally parallel after `2.4` + `3.1`.
- `4.1` can overlap provider projections once resolver/capability contracts stabilize.
- `4.2`/`4.3` are integration ratchets and should follow at least one working provider path.
- `5.1`-`5.4` can largely overlap after provider projection is stable.
- `6.1` can start before all providers finish; `6.2`/`6.3` are final serial gates.

The implementation model should keep commits/work units narrow along these boundaries. Do not collapse provider projection, core identity changes, connector ABI changes, and unrelated refactors into one large reasoning-heavy step.
