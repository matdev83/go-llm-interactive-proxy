# Implementation Plan

Implementation follows TDD throughout. Characterization tests, architecture gates, canonical interfaces, profile fixtures, state-machine goldens, storage contracts, and conformance cases are written before production behavior. A task is complete only when its focused validation passes and its observable completion condition is demonstrated.

Tasks use the architecture boundaries and contracts from `design.md`. No task may replace the selected design with an OpenAI Responses alias, a raw wire tunnel, direct frontend-to-backend invocation, raw upstream continuation IDs, or arbitrary provider pass-through.

## Phase 1: Lock Protocol, Canonical, and Architecture Contracts

- [ ] 1. Establish immutable protocol and brownfield contracts

- [ ] 1.1 Pin the official OpenResponses profile and dependency evidence
  - Write failing source-manifest tests that require the reviewed `2026-04-24` profile, immutable upstream commit, schema/compliance digests, Apache-2.0 attribution, and explicit profile deviations.
  - Add reproducible repository-owned profile fixtures for the normative schema, required response presence, official examples, and compliance scenario metadata without downloading mutable sources during tests.
  - Add a dependency decision test or policy gate that rejects source-unavailable, unlicensed, untagged-without-exception, or non-approved runtime modules.
  - Record the accepted and rejected Go implementation candidates in machine-reviewable dependency metadata and keep the OpenAI SDK confined to its existing protocol family.
  - Observable completion: changing the pinned commit, schema digest, profile version, or license provenance without updating the reviewed manifest fails focused tests.
  - _Requirements: 1.2, 1.3, 1.4, 1.5, 11.1, 11.2, 11.3, 11.4, 11.5, 11.6, 11.8, 12.3, 12.4_
  - _Boundary: Protocol source and dependency policy_
  - _Depends: none_
  - _Validation: `go test ./internal/plugins/protocols/openresponses/... -run 'Profile|Source|License|Dependency'`_

- [ ] 1.2 Characterize existing OpenAI Responses behavior before sharing helpers
  - Write request, response, SSE, reasoning replay, OpenRouter residual-control, error, cancellation, and route-mount characterization tests against the current OpenAI Responses frontend and native/generic backends.
  - Capture golden fixtures for required current behavior, including exact operation identity and the existing client/backend route contract.
  - Add architecture tests that fail when OpenResponses is registered as `openai.responses`, when either protocol imports the other's adapter package, or when OpenResponses wire types escape into canonical/public contracts.
  - Establish differential test helpers that compare only deliberately shared low-level behavior and treat protocol-specific defaults/resources as separate goldens.
  - Observable completion: an intentional OpenAI Responses wire/default change fails a characterization or differential test before OpenResponses production code exists.
  - _Requirements: 1.1, 1.6, 1.8, 11.4, 11.7, 11.9, 12.1, 12.5_
  - _Boundary: Existing OpenAI adapter tests and architecture gates_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/plugins/frontends/openairesponses/... ./internal/plugins/backends/openairesponses/... ./internal/plugins/backends/openaicompat/... ./internal/archtest/...`_

- [ ] 1.3 Define ordered canonical item, content, phase, and extension contracts with tests first
  - Write failing validation and round-trip tests for messages, item references, function calls, structured function outputs, reasoning, compaction, assistant phases, portable content, item identity/status, and bounded opaque extensions.
  - Add one-authority tests for ordered items versus legacy instruction/message fields and explicit lossless legacy projection tests.
  - Add normalized ordered walkers and tests proving capabilities, limits, hooks, redaction, counting, audit, and diagnostics cannot skip item-form calls.
  - Add deterministic byte, depth, count, reference, annotation, reasoning, compaction, and opaque extension bounds.
  - Keep all types protocol-neutral and prevent OpenResponses/OpenAI/provider wire structs from entering canonical contracts.
  - Observable completion: every portable trajectory round-trips canonically, ambiguous dual authority fails, and a non-representable legacy projection fails before execution.
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 3.8, 3.9, 3.10, 3.11, 3.12, 4.3, 4.4, 4.5, 10.7, 11.7, 12.1_
  - _Boundary: Canonical API, shared walkers, and security projections_
  - _Depends: 1.2_
  - _Validation: `go test ./pkg/lipapi/... ./pkg/lipsdk/... ./internal/core/capabilities/... ./internal/core/audit/... ./internal/core/tokenaccounting/...`_

- [ ] 1.4 Define operation, capability, dialect, and plugin-ABI contracts
  - Write failing negotiation tests for OpenResponses create, context compaction, ordered items, assistant phase, video, structured tool output, item references, reasoning/compaction dialects, and implementor-bound extensions.
  - Add backend declarations and candidate requirements that distinguish low-cardinality semantic capabilities from bounded exact dialect/implementor sets.
  - Prove pre-output failover considers only candidates satisfying the complete requirement set and that post-output failover remains prohibited.
  - Revalidate the active external backend plugin protocol and conformance DTOs; add versioned operation/item/dialect/compaction fields if the existing ABI cannot express the contracts.
  - Preserve one stream-first backend execution port for create and compaction unless the failing contract tests prove a separate port is required.
  - Observable completion: an incompatible backend is rejected before upstream work, while an external fake connector can advertise and execute every supported operation without importing internal types.
  - _Requirements: 1.1, 4.1, 4.6, 4.7, 4.8, 7.1, 7.3, 7.10, 9.9, 9.10, 11.7, 12.8, 12.9_
  - _Boundary: Canonical operations, capability negotiation, and public backend plugin ABI_
  - _Depends: 1.3_
  - _Validation: `go test ./pkg/lipapi/... ./internal/core/capabilities/... ./internal/core/routing/... ./pkg/lipsdk/backendplugin/...`_

- [ ] 1.5 Define route-claim and continuation service ports with contract tests
  - Write failing tests for normalized method/path claims, duplicate ownership, non-colliding default paths, canonical-path takeover, and deterministic two-owner diagnostics.
  - Define protocol-neutral response ID, scope, continuation record, storage policy, and continuation-store contracts without wire, HTTP, WebSocket, or provider types.
  - Write contract tests for high-entropy IDs, tenant/session scope, indistinguishable missing/unauthorized lookup, TTL, chain bounds, cycles, materialization limits, and idempotent deletion.
  - Define stream-recording ownership so terminal input/output can be persisted without buffering streaming delivery or exposing native provider references.
  - Observable completion: in-memory contract implementations pass route and continuation suites, including cross-scope probing and oversized-chain rejection.
  - _Requirements: 2.2, 2.3, 2.4, 2.5, 6.1, 6.2, 6.3, 6.8, 6.9, 6.10, 6.11, 6.12, 10.8, 10.12, 12.1_
  - _Boundary: HTTP composition contracts and core continuation application ports_
  - _Depends: 1.3_
  - _Validation: `go test ./internal/stdhttp/contract/... ./internal/core/continuation/... ./pkg/lipsdk/...`_

## Phase 2: Build the Pinned Wire Codec and State Machine

- [ ] 2. Implement the project-owned OpenResponses profile codec

- [ ] 2.1 Implement request, item, content, tool, and control codecs from failing goldens
  - Add typed codecs for portable request fields, item/content unions, function tools, every pinned portable `tool_choice` form, reasoning, phase, and structured tool outputs.
  - Preserve absent, explicit-null, and zero/default distinctions required by the pinned profile.
  - Implement the documented precedence rule for recognized dated provider-derived types and newly encountered implementor-prefixed opaque variants.
  - Reject unknown unprefixed variants, unsupported background/conversation modes, invalid discriminator combinations, and unapproved top-level extensions.
  - Map only semantic common fields into canonical controls; retain bounded profile residual controls with explicit candidate requirements.
  - Observable completion: official request examples and negative fixtures decode deterministically to canonical values and re-encode without losing required meaning.
  - _Requirements: 2.6, 2.7, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 4.1, 4.2, 4.3, 4.4, 4.9, 4.10, 10.7, 10.9, 11.5, 11.6_
  - _Boundary: Shared OpenResponses wire/profile package_
  - _Depends: 1.1, 1.3, 1.4_
  - _Validation: `go test ./internal/plugins/protocols/openresponses/... -run 'Request|Item|Content|Tool|Control|Presence'`_

- [ ] 2.2 (P) Implement response and compaction resource builders from required-field goldens
  - Write failing required-presence tests for every pinned `ResponseResource` and `response.compaction` field, including explicit null/default behavior.
  - Build response and compact resource types from frontend envelope metadata, canonical trajectories, usage, errors, controls, and timestamps.
  - Preserve item ordering, identity, lifecycle status, assistant phase, annotations, reasoning, compaction, and opaque extensions.
  - Ensure compact resources contain reusable output items and required usage while never implying continuation of the pre-compaction chain.
  - Observable completion: official response/compact examples and exhaustive presence fixtures validate against the pinned schema.
  - _Requirements: 2.8, 3.2, 3.4, 3.7, 5.5, 5.9, 7.2, 7.5, 7.6, 7.7, 12.4_
  - _Boundary: Shared OpenResponses resource codec_
  - _Depends: 1.3, 1.4_
  - _Validation: `go test ./internal/plugins/protocols/openresponses/... -run 'ResponseResource|CompactResource|RequiredPresence'`_

- [ ] 2.3 Implement the semantic event state machine and SSE framing
  - Write failing sequence tests for response, item, content-part, output-text, refusal, reasoning, function-argument, error, and terminal events.
  - Implement one state machine that consumes canonical events and produces both final resources and streaming events with consistent IDs, indices, status, phase, and sequence numbers.
  - Add lifecycle validation for missing starts, duplicate done events, discriminator/event-name mismatch, duplicate terminal, and events after terminal.
  - Add SSE serialization where `event` equals body `type`, the terminal response event precedes literal `[DONE]`, and no SSE `id` authority is introduced.
  - Add a conservative legacy-stream normalizer that synthesizes only unambiguous message/text/tool lifecycle and never invents phase, compaction, replay, or extensions.
  - Observable completion: the same scripted canonical stream produces equivalent non-streaming output and valid SSE event/resource goldens.
  - _Requirements: 4.12, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 5.9, 5.10, 10.7, 12.4_
  - _Boundary: Shared protocol state machine and SSE primitives_
  - _Depends: 2.1, 2.2_
  - _Validation: `go test ./internal/plugins/protocols/openresponses/... -run 'StateMachine|SSE|Lifecycle|Sequence|Terminal'`_

- [ ] 2.4 (P) Implement profile errors, limits, and fuzz targets
  - Write table tests for HTTP, SSE, and WebSocket error envelopes and stable mapping from internal validation, capability, continuation, rate-limit, model, transport, and committed failures.
  - Add independent request/resource/event/item/content/schema/metadata/annotation/opaque/continuation limits and sanitize every provider-originated message.
  - Add fuzz targets for JSON unions, absent/null states, discriminator conflicts, unknown extensions, SSE framing, event order, compact payloads, and malformed error objects.
  - Prove invalid or oversized data fails without leaking request content, provider opaque payloads, native IDs, or unbounded error strings.
  - Observable completion: all negative corpora produce stable bounded errors and fuzzing finds no panic, excessive allocation, or post-terminal acceptance.
  - _Requirements: 2.6, 5.6, 5.7, 10.6, 10.7, 10.9, 10.12, 12.6_
  - _Boundary: Shared protocol validation and error mapping_
  - _Depends: 2.1_
  - _Validation: `go test ./internal/plugins/protocols/openresponses/... -run 'Error|Limit|Redact' && go test -fuzz=Fuzz -fuzztime=30s ./internal/plugins/protocols/openresponses/...`_

- [ ] 2.5 Build a deterministic reference endpoint and Go-native conformance harness
  - Create a test-only endpoint that scripts JSON, SSE, compact, standard/opaque items, malformed events, slow output, cancellation, native IDs, and provider errors.
  - Mirror the pinned official scenario inputs and validators in Go-native tests so normal unit/integration validation does not require Node.
  - Add a separate immutable official-suite runner configuration for final deployment-level compliance.
  - Ensure the reference endpoint and harness expose no provider SDK and can be reused by frontend, backend, WebSocket, plugin-ABI, and end-to-end tests.
  - Observable completion: the reference endpoint passes all supported Go-native official-profile scenarios and intentionally broken modes fail with actionable diagnostics.
  - _Requirements: 11.6, 11.10, 12.2, 12.3, 12.4, 12.11_
  - _Boundary: Test kit and conformance infrastructure_
  - _Depends: 2.1, 2.2, 2.3, 2.4_
  - _Validation: `go test ./internal/testkit/openresponses/... ./internal/plugins/protocols/openresponses/conformance/...`_

## Phase 3: Implement the Client-Facing HTTP and SSE Frontend

- [ ] 3. Add non-colliding OpenResponses HTTP/SSE service

- [ ] 3.1 Implement strict frontend configuration and route ownership
  - Write failing config tests for supported profile, base path normalization, canonical-path takeover, WebSocket toggles, continuation limits, unknown fields, and development-only origin relaxation.
  - Implement frontend route claims and compose them with every enabled HTTP frontend before any handler is registered.
  - Reject method/path collisions deterministically with both frontend owners and preserve the existing OpenAI Responses route unchanged.
  - Expose sanitized profile/base-path/transport/continuation configuration in diagnostics without allocating runtime state during check-config.
  - Observable completion: both frontends coexist on default paths, explicit collision fails before serving, and route registration order cannot change the result.
  - _Requirements: 1.4, 1.7, 2.1, 2.2, 2.3, 2.4, 2.5, 10.1, 10.3, 10.5, 10.11_
  - _Boundary: Frontend config, HTTP route catalog, and composition root_
  - _Depends: 1.5, 2.1_
  - _Validation: `go test ./internal/plugins/frontends/openresponses/... ./internal/stdhttp/... -run 'Config|Route|Collision|Coexist'`_

- [ ] 3.2 Implement authenticated create-request decoding and canonical call construction
  - Write failing handler tests proving authentication and authoritative route/session resolution occur before body processing or continuation lookup.
  - Decode JSON string/item inputs, tools, controls, metadata policy, residual extensions, and conditional model/input validity through the pinned codec.
  - Construct one authoritative ordered canonical trajectory, derive capabilities/dialects, and reject unsupported background/conversation/proprietary behavior before execution.
  - Preserve client identity for diagnostics/audit without allowing body model, metadata, user, safety, cache, response ID, or extension fields to become route/session authority.
  - Apply standard body, nesting, item, tool, and content limits without echoing sensitive input in errors.
  - Observable completion: valid official create requests reach a stub executor as exact canonical calls and invalid/unauthorized requests cause zero executor or state-store work.
  - _Requirements: 2.6, 2.7, 2.10, 3.1, 3.3, 3.4, 3.5, 4.1, 4.9, 4.10, 10.7, 10.8, 10.9_
  - _Boundary: OpenResponses frontend decode adapter_
  - _Depends: 2.1, 3.1_
  - _Validation: `go test ./internal/plugins/frontends/openresponses/... -run 'Decode|Auth|Authority|CreateRequest'`_

- [ ] 3.3 Implement non-streaming response envelopes and complete JSON resources
  - Write failing tests for proxy response ID allocation, timestamps, model echo, previous-response linkage, required null/default fields, output ordering, usage, errors, and incomplete status.
  - Execute through the standard executor and collect with explicit OpenResponses aggregation limits.
  - Build the final resource through the shared state machine rather than a separate sparse response path.
  - Keep native provider response IDs private and prove they cannot replace or predict client-facing IDs.
  - Return `application/json` and profile-shaped errors with no OpenAI-specific cancel/resource behavior.
  - Observable completion: official basic, system, tool, image, multi-turn, and phase resource scenarios pass through a stub executor.
  - _Requirements: 2.8, 2.9, 5.5, 5.9, 5.10, 6.1, 10.6, 10.7_
  - _Boundary: OpenResponses frontend non-streaming adapter_
  - _Depends: 2.2, 2.3, 3.2_
  - _Validation: `go test ./internal/plugins/frontends/openresponses/... -run 'NonStreaming|ResponseResource|ProxyID|RequiredFields'`_

- [ ] 3.4 Implement incremental SSE delivery and terminal ownership
  - Write failing integration tests for semantic event order, item/content lifecycle, usage, structured error plus failed terminal, literal `[DONE]`, slow clients, write failures, and cancellation.
  - Stream canonical events incrementally through the shared state machine without collecting the full response.
  - Enforce first-visible-output commitment and prevent retry/failover, duplicate terminal, or post-terminal writes after any client-visible event.
  - Close backend streams, response recorders, and HTTP resources exactly once on completion, cancellation, panic, or writer failure.
  - Observable completion: streaming official-profile cases pass under slow consumption with bounded memory and no goroutine or stream leaks.
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.6, 5.7, 5.8, 5.9, 5.10, 10.7, 10.10, 12.7, 12.8_
  - _Boundary: OpenResponses frontend SSE adapter and terminal wrapper_
  - _Depends: 2.3, 2.4, 3.2_
  - _Validation: `go test -race ./internal/plugins/frontends/openresponses/... -run 'SSE|Backpressure|Cancel|WriteFailure|Terminal'`_

- [ ] 3.5 Prove OpenAI/OpenResponses frontend isolation and differential behavior
  - Run both frontends in one composed runtime and verify default and explicit non-conflicting routes, authentication, content types, resources, and error shapes.
  - Add differential cases for deliberately shared text/tool/reasoning/usage behavior while asserting distinct operation IDs, response defaults, route ownership, extension policy, and unsupported features.
  - Prove OpenResponses requests cannot reach OpenAI-specific cancel handling and OpenAI requests cannot opt into OpenResponses behavior through body/header/user-agent changes.
  - Observable completion: all existing OpenAI Responses characterization fixtures pass unchanged alongside OpenResponses frontend tests.
  - _Requirements: 1.1, 1.6, 1.8, 2.4, 2.5, 2.9, 12.5_
  - _Boundary: Cross-frontend integration tests_
  - _Depends: 3.3, 3.4_
  - _Validation: `go test ./internal/stdhttp/... ./internal/plugins/frontends/openairesponses/... ./internal/plugins/frontends/openresponses/... -run 'Coexist|Differential|Isolation'`_

## Phase 4: Implement Continuation and Context Compaction

- [ ] 4. Add proxy-owned response state and compact routing

- [ ] 4.1 Implement bounded continuation storage and proxy response IDs
  - Write storage contract tests first for reservation, terminal put, scoped get/delete, TTL, cleanup, restart behavior where supported, idempotency, and storage failure.
  - Implement high-entropy proxy IDs and tenant/session-scoped keys using existing secure state policy or an equally reviewed adapter.
  - Store canonical profile, input/output trajectory, controls, route/model lineage, dialect requirements, terminal status, and protected native references without logging payloads.
  - Enforce per-record, per-scope, total, expiry, chain-depth, item, and materialized-byte bounds.
  - Ensure failed responses are not valid continuation parents and define tested incomplete-response eligibility.
  - Observable completion: durable and in-memory contract implementations survive restart/expiry/collision/security tests and leak no cross-scope existence information.
  - _Requirements: 6.1, 6.3, 6.8, 6.9, 6.10, 6.11, 6.12, 10.6, 10.7, 10.12_
  - _Boundary: Continuation application service and infrastructure store adapters_
  - _Depends: 1.5, 3.3, 3.4_
  - _Validation: `go test -race ./internal/core/continuation/... ./internal/infra/continuation/... -run 'Store|Scope|TTL|Restart|Limit|ID'`_

- [ ] 4.2 Implement continuation materialization, recording, and routing constraints
  - Write failing tests for logical previous-input → previous-output → new-input order, profile compatibility, portable rerouting, provider-bound pinning, native-reference optimization, and canonical fallback.
  - Resolve continuation only after authentication and before candidate selection, merging item/dialect requirements and enforcing chain/materialization bounds.
  - Add a stream recorder that captures terminal canonical output while forwarding events incrementally and persists according to storage policy without changing output commitment.
  - Keep client response IDs separate from private native references and never forward the client ID to a generic remote provider.
  - Prove pre-output failure can choose another fully compatible candidate and post-output failure cannot replay the turn.
  - Observable completion: portable history can route across compatible backends, provider-bound history remains safely pinned/rejected, and output streaming remains incremental.
  - _Requirements: 4.6, 4.7, 4.8, 6.2, 6.3, 6.4, 6.5, 6.6, 6.7, 6.12, 12.8_
  - _Boundary: Continuation materializer/recorder and core candidate requirements_
  - _Depends: 1.4, 4.1_
  - _Validation: `go test ./internal/core/continuation/... ./internal/core/routing/... -run 'Materialize|Portable|Pinned|Native|Record|Failover'`_

- [ ] 4.3 Integrate HTTP `previous_response_id` and storage policy
  - Write failing frontend tests for `store:true`, omitted/default storage, successful continuation, missing/expired/unauthorized/incompatible IDs, previous-response echo, and no state work for unauthorized requests.
  - Reserve the new response envelope, resolve the parent, execute the materialized call, and record the terminal trajectory through the continuation service.
  - Return `previous_response_not_found` for every non-resolvable parent without revealing tenant/backend/profile distinctions.
  - Ensure metadata, response ID, and native lineage cannot override authoritative session or route selection.
  - Observable completion: the official semantic multi-turn flow works with only new input while cross-scope and expired references fail identically.
  - _Requirements: 2.7, 6.1, 6.2, 6.3, 6.8, 6.9, 6.10, 10.8, 10.9_
  - _Boundary: OpenResponses HTTP frontend and continuation service integration_
  - _Depends: 4.2_
  - _Validation: `go test ./internal/plugins/frontends/openresponses/... ./internal/core/continuation/... -run 'PreviousResponse|StoreTrue|NotFound|Scope'`_

- [ ] 4.4 Implement context compaction through the normal executor
  - Write failing operation/capability tests for model-required validation, compact item input, backend eligibility, pre-output failover, usage, required compact resource fields, and unsupported capability.
  - Map compact requests to the protocol-neutral context-compaction operation and route them through the same executor/backend stream lifecycle as create requests.
  - Collect compacted ordered items with explicit limits and build `response.compaction` through the shared resource codec.
  - Enforce that later use starts a new response chain and that compaction dialects bind later candidates without reusing the old response ID.
  - Return a protocol-shaped unsupported-capability error rather than emulating summarization.
  - Observable completion: official compact and missing-model cases pass through two candidate backends, including recoverable pre-output failure and exact usage/output preservation.
  - _Requirements: 7.1, 7.2, 7.3, 7.5, 7.6, 7.7, 7.8, 7.9, 12.2, 12.8_
  - _Boundary: OpenResponses compact frontend, canonical operation, and executor integration_
  - _Depends: 1.4, 2.2, 4.2_
  - _Validation: `go test ./internal/plugins/frontends/openresponses/... ./internal/core/runtime/... ./internal/core/routing/... -run 'Compact|Compaction|MissingModel|Failover'`_

- [ ] 4.5 Harden continuation and compaction security, races, and lifecycle
  - Add adversarial tests for response-ID probing, fixation, cycles, depth/byte amplification, expired native references, storage write failure, cancellation during persistence, and opaque replay redaction.
  - Add race/leak tests for concurrent lookups, terminal recorder close, HTTP disconnect, reload generation changes, durable-store shutdown, and compaction collection failure.
  - Ensure persistence failures follow the selected consistency policy without duplicate client terminal events or resource leaks.
  - Observable completion: targeted race/leak suites leave no record, stream, goroutine, lock, or native-reference leak and all outward errors remain bounded.
  - _Requirements: 6.8, 6.9, 6.10, 6.11, 6.12, 7.8, 10.10, 10.11, 10.12, 12.6, 12.7, 12.8_
  - _Boundary: Continuation/compaction security and lifecycle tests_
  - _Depends: 4.3, 4.4_
  - _Validation: `go test -race ./internal/core/continuation/... ./internal/infra/continuation/... ./internal/plugins/frontends/openresponses/... -run 'Security|Race|Leak|Reload|Shutdown'`_

## Phase 5: Implement the Generic Remote OpenResponses Backend

- [ ] 5. Add dependency-free remote provider/router connectivity

- [ ] 5.1 Implement strict generic backend configuration and built-in-compatible registration
  - Write failing config tests for stable factory kind, independent instance ID/prefix, profile, endpoint, environment-only credentials, no-auth mode, inventory, capability/dialect declarations, limits, unknown fields, and prefix collisions.
  - Register the mode through the final built-in protocol-family composition boundary without adding provider SDKs, external plugin requirements, or core branches.
  - Reuse common endpoint, credential, admission, inventory, ownership, and diagnostics infrastructure from compatible modes.
  - Publish conservative portable capabilities by default and require explicit bounded declarations for compaction, phase, video, native continuation, recognized types, and extension slugs.
  - Observable completion: multiple independent instances construct with separate credentials/limits/provenance, no-auth emits no empty auth header, and root builds without external connector modules.
  - _Requirements: 9.1, 9.2, 9.4, 9.5, 9.6, 9.9, 10.2, 10.3, 10.4, 10.5, 11.9, 11.10_
  - _Boundary: Built-in compatible backend composition and shared infrastructure_
  - _Depends: 1.4, 2.1, 3.1_
  - _Validation: `go test ./internal/standardplugins/... ./internal/plugins/backends/openresponsescompat/... -run 'Config|Factory|NoAuth|Instance|Prefix|Caps' && GOWORK=off go build ./cmd/lipstd`_

- [ ] 5.2 Implement create request mapping and non-streaming response adaptation
  - Write failing mapping tests for ordered portable items, controls, structured tool outputs, phase, reasoning replay, residual allowlists, endpoint paths, and unsupported semantics.
  - Map canonical create calls to pinned wire requests without forwarding proxy response IDs or arbitrary top-level/header data.
  - Apply credentials, route/model resolution, common HTTP policy, and provider wrapper options through explicit seams.
  - Parse complete response resources into canonical item lifecycle, usage, status, error, and private native-reference evidence.
  - Reject capability/dialect mismatches before opening the upstream request.
  - Observable completion: the deterministic reference endpoint receives exact schema-valid JSON and returns a lossless canonical trajectory through the backend port.
  - _Requirements: 4.9, 4.10, 4.11, 6.6, 6.7, 9.3, 9.4, 9.5, 9.6, 9.10, 9.11_
  - _Boundary: Generic OpenResponses backend request/JSON adapter_
  - _Depends: 2.1, 2.2, 5.1_
  - _Validation: `go test ./internal/plugins/backends/openresponsescompat/... -run 'Request|NonStreaming|Mapping|Endpoint|Unsupported|NativeRef'`_

- [ ] 5.3 Implement SSE event mapping, opaque extension output, and commitment behavior
  - Write failing stream tests for every claimed standard event, unknown valid prefixed events/items, malformed/unprefixed events, event-name mismatch, duplicate terminal, slow consumers, cancellation, and provider disconnect.
  - Parse incrementally with bounded events and map full item/content lifecycle into canonical events without collecting the response.
  - Preserve unknown provider-prefixed output as bounded opaque canonical extensions and reject incompatible or malformed data rather than dropping it.
  - Peek the first canonical event before returning the stream so authentication, rate-limit, invalid request, and transport failures retain existing pre-output classification.
  - Prove provider or credential retry occurs only before visible output and committed failures remain on the selected attempt.
  - Observable completion: reference SSE streams preserve ordering/extensions under backpressure and all post-output failures are observable without replay.
  - _Requirements: 4.5, 4.6, 4.7, 4.8, 4.12, 5.5, 5.7, 5.8, 9.7, 9.8, 12.7, 12.8_
  - _Boundary: Generic OpenResponses backend SSE adapter_
  - _Depends: 2.3, 2.4, 5.2_
  - _Validation: `go test -race ./internal/plugins/backends/openresponsescompat/... -run 'SSE|Extension|Backpressure|Cancel|Commit|Retry'`_

- [ ] 5.4 Implement remote compaction and operation-specific capabilities
  - Write failing tests for compact endpoint joining, model-required request, ordered input/replay, usage, compact item output, malformed resource, unsupported configuration, and pre-output failover classification.
  - Map the canonical context-compaction operation to remote `responses/compact` using the same endpoint, credentials, limits, errors, and item codec as create.
  - Advertise compaction only when explicitly configured/model-supported and emit canonical compacted item events through the existing managed stream port.
  - Preserve compaction dialect and private lineage requirements for later candidate negotiation.
  - Observable completion: frontend-to-core-to-generic-backend compaction passes the official compact scenario and a non-compaction instance is rejected before network work.
  - _Requirements: 7.3, 7.4, 7.8, 7.9, 7.10, 9.3, 9.8, 9.9, 9.10_
  - _Boundary: Generic OpenResponses backend compact adapter and capability profile_
  - _Depends: 4.4, 5.2_
  - _Validation: `go test ./internal/plugins/backends/openresponsescompat/... ./internal/core/routing/... -run 'Compact|CompactionCaps|Dialect|NoNetwork'`_

- [ ] 5.5 Enforce provider-specific connector boundaries and inventory provenance
  - Define and test the narrow shared-codec customization seam for provider connectors to add typed headers, routing controls, errors, billing, catalog, and extensions without generic pass-through.
  - Add tests proving OpenRouter-specific attribution, provider ordering/fallback, billing, inventory, and proprietary controls are rejected by generic config and remain owned by its external connector.
  - Preserve backend instance, factory kind, route prefix, profile, model source/freshness, capability evidence, and extension dialects in inventory/diagnostics.
  - Ensure standard model discovery or static inventory uses the same validated endpoint/credentials and does not activate external provider processes for the generic mode.
  - Observable completion: a provider wrapper can reuse the codec through explicit options while architecture tests prevent reverse imports or generic policy leakage.
  - _Requirements: 1.7, 4.11, 9.2, 9.4, 9.12, 10.5, 11.9, 12.9_
  - _Boundary: Shared codec options, provider connector boundary, and inventory_
  - _Depends: 5.1, 5.2, 5.3_
  - _Validation: `go test ./internal/plugins/backends/openresponsescompat/... ./internal/core/modelregistry/... ./internal/archtest/... -run 'ProviderBoundary|OpenRouter|Inventory|Provenance'`_

## Phase 6: Implement the Client-Facing WebSocket Transport

- [ ] 6. Add persistent sequential OpenResponses client sessions

- [ ] 6.1 Implement authenticated upgrade, origin policy, and connection limits
  - Write failing tests for non-upgrade GET, missing/invalid authentication, explicit origins, development-only relaxed origin, profile/base-path routing, message limits, idle timeout, ping/pong, and maximum age.
  - Upgrade only after authentication and origin validation using the approved WebSocket dependency behind a testable transport boundary.
  - Allocate bounded connection-local continuation state only after successful authorization.
  - Enforce a configurable maximum age not exceeding 60 minutes and bounded reader/writer resources.
  - Observable completion: unauthorized or disallowed-origin attempts allocate no session/state, and valid server-side clients establish one bounded session.
  - _Requirements: 8.1, 8.8, 8.9, 8.12, 10.1, 10.7, 10.8_
  - _Boundary: OpenResponses frontend WebSocket upgrade and transport_
  - _Depends: 3.1, 4.1_
  - _Validation: `go test ./internal/plugins/frontends/openresponses/... -run 'WebSocketUpgrade|Auth|Origin|Limit|Ping|Age'`_

- [ ] 6.2 Implement sequential `response.create` turn execution and shared event output
  - Write failing state-machine tests for valid create messages, forbidden HTTP-only fields, one active turn, bounded queued turns, sequential reuse, and invalid message envelopes.
  - Use one session owner to decode, materialize, execute, and stream each turn through the same canonical executor and event encoder used by HTTP/SSE.
  - Emit raw OpenResponses event objects without SSE framing or `[DONE]` and preserve identical lifecycle, sequence, IDs, status, phase, usage, and terminal response semantics.
  - Reject multiplexing and prevent the next turn from starting until terminal ownership of the current turn is complete.
  - Observable completion: official basic and sequential WebSocket scenarios pass and concurrent create attempts cannot create two in-flight executor streams.
  - _Requirements: 8.2, 8.3, 8.4, 8.9, 8.11, 12.2_
  - _Boundary: OpenResponses WebSocket turn runner_
  - _Depends: 2.3, 3.2, 6.1_
  - _Validation: `go test -race ./internal/plugins/frontends/openresponses/... -run 'WebSocketTurn|Sequential|NoMultiplex|EventParity'`_

- [ ] 6.3 Implement connection-local `store:false` continuation and eviction
  - Write failing tests for local response recording, continuation with only new input, reconnect loss, missing parent, failed-continuation eviction, successful parent retention, byte/count bounds, and socket-close cleanup.
  - Reuse the continuation contracts with a connection-local store implementation that can never persist `store:false` state.
  - Return the required WebSocket error envelope and `previous_response_not_found` code for unavailable local state.
  - Evict the referenced parent after a continuation turn fails with a client/server error and prove a later reuse fails.
  - Integrate compact output as a new base input without the pre-compaction response ID.
  - Observable completion: every pinned WebSocket continuation, reconnect, eviction, and compact-new-chain scenario passes.
  - _Requirements: 6.9, 7.7, 8.5, 8.6, 8.7, 8.9, 12.2_
  - _Boundary: WebSocket connection-local continuation store and turn integration_
  - _Depends: 4.2, 4.4, 6.2_
  - _Validation: `go test ./internal/plugins/frontends/openresponses/... -run 'WebSocketContinuation|Reconnect|Evict|CompactNewChain|StoreFalse'`_

- [ ] 6.4 Implement connection-age errors, disconnect cancellation, and terminal cleanup
  - Write failing tests for maximum-age expiry, `websocket_connection_limit_reached`, idle close, client disconnect before/after output, writer failure, backend block, and server shutdown.
  - Cancel the active canonical attempt on disconnect and retain standard no-retry-after-output behavior.
  - Emit protocol-shaped errors before close when writable, then close reader, writer, active stream, queue, and local state exactly once.
  - Ensure old runtime-generation sockets do not switch executor or continuation ownership during reload.
  - Observable completion: all closure paths terminate promptly with no duplicate terminal event, cross-generation work, or leaked goroutine/socket/state.
  - _Requirements: 8.8, 8.9, 8.10, 10.10, 10.11, 12.7, 12.8_
  - _Boundary: WebSocket lifecycle, runtime generation, and cancellation_
  - _Depends: 6.2, 6.3_
  - _Validation: `go test -race ./internal/plugins/frontends/openresponses/... ./internal/infra/runtimebundle/... -run 'WebSocketClose|ConnectionLimit|Disconnect|Reload|Shutdown|Leak'`_

- [ ] 6.5 Fuzz and stress the WebSocket session state machine
  - Add fuzz corpora for malformed JSON, message unions, forbidden fields, oversized fragments, invalid UTF-8, repeated creates, unexpected binary/control frames, error envelopes, and lifecycle transitions.
  - Add slow-reader/slow-writer, queue saturation, local-state saturation, and one-hour-limit virtual-clock stress tests with bounded memory.
  - Prove one reader/one writer ownership, no concurrent map mutation, no deadlock, and deterministic cleanup under cancellation races.
  - Observable completion: fuzz/stress tests find no panic, data race, unbounded queue, duplicate execution, or retained local state.
  - _Requirements: 8.3, 8.8, 8.9, 8.10, 10.7, 10.10, 12.6, 12.7_
  - _Boundary: WebSocket robustness tests_
  - _Depends: 6.4_
  - _Validation: `go test -race ./internal/plugins/frontends/openresponses/... -run 'WebSocketStress|Queue|Slow' && go test -fuzz=FuzzWebSocket -fuzztime=30s ./internal/plugins/frontends/openresponses/...`_

## Phase 7: Integrate, Revalidate, and Prove Release Readiness

- [ ] 7. Complete standard-runtime integration and conformance

- [ ] 7.1 Integrate standard registration, diagnostics, reload, and shutdown
  - Register the frontend and generic backend through standard composition with immutable route/prefix ownership and no core protocol branches.
  - Add sanitized diagnostics for profile, frontend/backend origin, base path/endpoint, transports, continuation mode, capability/dialect declarations, inventory, and conformance state.
  - Integrate continuation store and WebSocket ownership into runtime build rollback, atomic reload, generation drain, and reverse-order shutdown.
  - Add check-config coverage that is structural and network-free and reports every conflicting owner or unsupported profile before serving.
  - Observable completion: a complete standard runtime can enable/disable/reload both surfaces without orphaned routes, stores, backends, WebSockets, or inventory state.
  - _Requirements: 1.7, 2.3, 9.2, 10.1, 10.2, 10.3, 10.5, 10.6, 10.10, 10.11_
  - _Boundary: Standard composition, diagnostics, and runtime lifecycle_
  - _Depends: 3.5, 4.5, 5.5, 6.4_
  - _Validation: `go test -race ./internal/standardplugins/... ./internal/stdhttp/... ./internal/infra/runtimebundle/... ./internal/core/diag/... -run 'OpenResponses|Reload|Rollback|Shutdown|Inspect'`_

- [ ] 7.2 Complete external backend plugin ABI and conformance integration
  - Implement any versioned plugin DTO/host changes identified in Task 1.4 for ordered items, dialect requirements, OpenResponses create, context compaction, opaque output events, and operation capabilities.
  - Update fake external connectors and conformance tests before migrating or adding a provider-specific OpenResponses connector.
  - Prove the root module and canonical core do not import external connector modules or provider SDKs and that unsupported older plugin versions fail before execution.
  - Preserve built-in-compatible classification for the generic endpoint while allowing external providers to reuse the same canonical operation contracts.
  - Observable completion: both built-in generic and external fake provider connectors pass equivalent canonical create/compact conformance with version negotiation.
  - _Requirements: 7.10, 9.1, 9.9, 11.7, 11.9, 11.10, 12.9_
  - _Boundary: Public backend plugin ABI, host adapter, and module architecture_
  - _Depends: 1.4, 5.4_
  - _Validation: `go test ./pkg/lipsdk/backendplugin/... ./internal/infra/backendplugins/... ./internal/archtest/... && GOWORK=off go build ./cmd/lipstd`_

- [ ] 7.3 Run full end-to-end and official pinned compliance scenarios
  - Build a reference configuration exercising the client frontend through core into the generic backend/reference provider for JSON, SSE, compact, and WebSocket.
  - Run every pinned official scenario and validate all enabled transports against the immutable suite source/digest.
  - Add machine-validated configuration/API examples that clearly distinguish OpenResponses, OpenAI Responses, canonical/non-colliding paths, and the unrelated `open-responses` project.
  - Store only bounded conformance result summaries and profile/source identifiers in diagnostics or CI artifacts.
  - Observable completion: the pinned official suite passes against a reference Go-LIP deployment and the examples are executed or schema-validated in CI.
  - _Requirements: 2.1, 9.3, 11.10, 12.2, 12.3, 12.4, 12.10, 12.11_
  - _Boundary: End-to-end test deployment, compliance runner, and validated examples_
  - _Depends: 7.1, 7.2_
  - _Validation: `go test ./internal/integration/openresponses/... && ./scripts/test-openresponses-compliance`_

- [ ] 7.4 Execute cross-cutting security, failover, and regression proofs
  - Run adversarial end-to-end cases for extension smuggling, incompatible provider failover, response-ID probing, native-ID leakage, route collision, origin abuse, continuation amplification, event injection, and sensitive opaque replay.
  - Prove every failure before output is classified consistently and every failure after visible SSE/WS/JSON commitment remains on the selected attempt.
  - Run full OpenAI Responses differential fixtures and verify no changed routes, defaults, reasoning replay, OpenRouter behavior, error shapes, or SDK boundaries.
  - Validate logs, metrics, audit snapshots, and diagnostics for redaction and bounded cardinality under malicious identifiers/types/errors.
  - Observable completion: security and regression matrices pass with no sensitive content/native ID leakage and no illegal retry/failover.
  - _Requirements: 4.6, 4.7, 4.8, 6.9, 10.6, 10.8, 10.9, 10.12, 12.5, 12.7, 12.8_
  - _Boundary: Cross-cutting integration, security, and regression tests_
  - _Depends: 7.3_
  - _Validation: `go test -race ./internal/integration/openresponses/... ./internal/core/... ./internal/plugins/frontends/openairesponses/... -run 'Security|Failover|Commitment|Redaction|Regression'`_

- [ ] 7.5 Run final architecture, quality, and minimal-distribution gates
  - Run focused package suites, full repository tests, race tests for changed concurrency packages, fuzz smoke tests, architecture tests, `go vet`, and static analysis required by repository steering.
  - Build the standard binary with `GOWORK=off`, no external connector modules, no JavaScript runtime, and both enabled/disabled OpenResponses configurations.
  - Verify source/license notices, generated-code reproducibility, no mutable network generation, no provider SDK leakage, and no unreviewed module additions.
  - Confirm requirement-to-task coverage, official conformance evidence, and no unresolved P0/P1 design-review finding.
  - Observable completion: all release gates pass from a clean checkout and the implementation can be shipped without external plugins or Node except for the separate optional compliance invocation.
  - _Requirements: 1.8, 11.1, 11.2, 11.5, 11.6, 11.9, 11.10, 12.1, 12.3, 12.6, 12.7, 12.12_
  - _Boundary: Repository-wide delivery gates_
  - _Depends: 7.4_
  - _Validation: `go test ./... && go test -race ./internal/plugins/frontends/openresponses/... ./internal/plugins/backends/openresponsescompat/... ./internal/core/continuation/... && go vet ./... && GOWORK=off go build ./cmd/lipstd`_
