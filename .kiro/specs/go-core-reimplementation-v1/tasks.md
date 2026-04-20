# Implementation Plan

## Task List

- [x] 1. Establish the repository skeleton, package boundaries, and TDD harness
  - Create the Go module, package roots, and composition root layout defined in the File Structure Plan.
  - Add baseline CI commands for `go test`, fuzz targets, and `-race`.
  - Write failing boundary tests that prove `lipcore` does not import bundled frontend, backend, or feature-plugin packages.
  - _Requirements: 1.1, 1.2, 1.5, 14.3, 14.5, 15.1_
  - _Boundary: cmd/lipstd, lipapi, lipsdk, lipcore, internal/testkit_

- [x] 1.1 Create the core package scaffolding and compile-only contracts
  - Write RED tests for package visibility, constructor wiring, and typed startup errors.
  - Add empty but compiling packages for `lipapi`, `lipsdk`, and `lipcore`.
  - _Requirements: 1.1, 1.2, 12.6, 14.4, 14.5, 15.1_

- [x] 1.2 (P) Build the shared test kit and CI execution profile
  - Add provider stub scaffolding, golden helpers, and canonical event assertions.
  - Add race, fuzz, and benchmark entrypoints to CI or documented local commands.
  - _Requirements: 15.2, 15.3, 15.4, 14.6_
  - _Boundary: internal/testkit, CI_
  - _Depends: 1.1_

- [x] 2. Define the canonical call, event, capability, and error model
  - Write failing contract tests that lock the shared request subset, event ordering, and collected non-streaming behavior.
  - Implement the minimal canonical types and typed error hierarchy in `lipapi`.
  - _Requirements: 2.1, 2.2, 2.3, 2.5, 5.2, 14.4, 15.1_
  - _Boundary: lipapi_

- [x] 2.1 Implement canonical request and part types
  - Define messages, parts, tool declarations, generation options, and vendor extensions—including explicit **multimodal** part kinds for the v1 shared subset (e.g. images, documents such as PDFs).
  - Add validation tests for invariants and unsupported combinations.
  - _Requirements: 2.1, 2.3, 2.6, 10.4, 14.4_

- [x] 2.2 Implement canonical event types and the collector contract
  - Define stream event families and collection rules for non-streaming responses.
  - Add RED/GREEN tests for event ordering, collection, and error termination.
  - _Requirements: 2.2, 5.1, 5.2, 11.1_

- [x] 2.3 Implement capability declarations and typed capability errors
  - Define the capability vocabulary and negotiation result types, including **multimodal** modalities relevant to the v1 shared subset.
  - Add tests for lossless, downgrade, and reject outcomes.
  - _Requirements: 2.4, 2.6, 2.7, 4.7, 12.2, 12.5_

- [x] 3. Build the stable plugin SDK and explicit registration model
  - Write failing tests for duplicate plugin IDs, missing mandatory plugins, and opaque plugin config handling.
  - Implement registration contracts for frontend, backend, submit-hook, part-hook, and tool-reactor plugins.
  - _Requirements: 1.3, 12.1, 12.2, 12.3, 12.5, 12.6, 14.5, 15.1_
  - _Boundary: lipsdk, lipcore/runtime_

- [x] 3.1 Implement plugin registration and lifecycle contracts
  - Add startup validation for uniqueness and mandatory bundle coverage.
  - Keep constructor wiring explicit in the composition root.
  - _Requirements: 12.1, 12.2, 12.5, 12.6_

- [x] 3.2 Implement opaque plugin config payload handling
  - Add config tests proving the core never needs plugin-private schema knowledge.
  - Pass raw config payloads through the plugin factory surface.
  - _Requirements: 12.3, 12.4, 14.4_

- [x] 4. Implement the stream engine, cancellation, and hook bus
  - Write failing tests for streaming pass-through, cancellation propagation, hook ordering, mutation validation, and fail-open/fail-closed behavior.
  - Implement `EventStream`, collector helpers, cancellation propagation, and the hook bus.
  - _Requirements: 5.1, 5.2, 5.3, 9.1, 9.4, 10.1, 10.2, 10.3, 11.3, 11.5, 14.1, 14.3_
  - _Boundary: lipcore/stream, lipcore/hooks_

- [x] 4.1 Implement submit hook execution
  - Add typed metadata, ordering, and rejection handling.
  - Prove via tests that the runtime works with zero registered hooks.
  - _Requirements: 9.1, 9.2, 9.3, 9.5_

- [x] 4.2 (P) Implement request and response part hook surfaces
  - Add canonical mutation validation and typed hook errors.
  - Add RED tests for invalid part rewrites and safe pass-through behavior.
  - _Requirements: 10.1, 10.2, 10.3, 10.4, 10.5_
  - _Boundary: lipcore/hooks, lipsdk/hooks_
  - _Depends: 2.1, 2.2_

- [x] 4.3 (P) Implement reserved tool-reactor orchestration surfaces
  - Add canonical tool event types, reactor decisions, and fail-open default behavior.
  - Add tests for pass-through, swallow, rewrite, and replace decisions at the interface level.
  - _Requirements: 11.1, 11.2, 11.3, 11.4, 11.5_
  - _Boundary: lipcore/hooks, lipsdk/hooks_
  - _Depends: 2.2_

- [x] 5. Implement the route selector parser, planner, and session-aware weighted routing
  - Write RED tests for explicit selectors, ordered failover, weighted routing, first-request annotations, invalid annotation combinations, and candidate exclusion after failure.
  - Implement the selector AST, parser, and planner.
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 7.1, 7.2, 7.3, 7.4, 14.4, 15.4_
  - _Boundary: lipcore/routing_

- [x] 5.1 Implement ordered failover planning and candidate exclusions
  - Add tests proving left-to-right failover and deterministic exclusion behavior.
  - _Requirements: 6.2, 6.4, 6.5_

- [x] 5.2 Implement weighted routing and first-request session rules
  - Add tests for single `[first]` enforcement, first-request consumption, and retry-path behavior.
  - _Requirements: 6.3, 7.1, 7.2, 7.3, 7.4, 7.5_

- [x] 6. Implement the B2BUA store, lineage model, and recovery policy
  - Write failing tests for A-leg resolution, B-leg allocation, continuity reuse, attempt recording, and in-memory expiration.
  - Implement the in-memory B2BUA store and typed lineage records.
  - _Requirements: 7.5, 8.1, 8.2, 8.4, 8.5, 8.6, 13.1, 13.2, 14.3, 14.6_
  - _Boundary: lipcore/b2bua_

- [x] 6.1 Implement A-leg continuity and first-request state retention
  - Add tests covering explicit continuity keys and safe new-session fallback.
  - _Requirements: 7.5, 8.1, 8.5, 8.6_

- [x] 6.2 Implement B-leg sequence allocation and attempt record queries
  - Add tests for monotonic sequencing, lineage reads, and surfaced/swallowed outcomes.
  - _Requirements: 8.2, 8.4, 13.2_

- [x] 7. Implement the core execution engine with pre-output recovery semantics
  - Write failing end-to-end executor tests for normal success, recoverable pre-output failure, post-output failure, cancellation, and collector-based non-streaming responses.
  - Implement the execution engine that combines hooks, capability checks, route planning, B2BUA, and backend invocation.
  - _Requirements: 5.1, 5.3, 5.4, 5.5, 6.4, 6.6, 8.3, 8.4, 9.1, 10.2, 11.1, 13.1, 13.3, 14.1_
  - _Boundary: lipcore/runtime_
  - _Depends: 2, 4, 5, 6_

- [x] 7.1 Implement capability negotiation in the executor path
  - Reject unsupported combinations before upstream calls begin.
  - _Requirements: 2.4, 4.7, 12.2_

- [x] 7.2 Implement recoverable pre-output failure swallowing and retry orchestration
  - Add tests that one client request may create multiple related backend attempts while surfacing one logical response.
  - _Requirements: 5.4, 5.5, 6.4, 8.3, 8.4_

- [x] 7.3 Implement post-output failover prohibition and cancellation propagation
  - Add tests proving no silent recovery after visible output has started.
  - _Requirements: 5.3, 5.4, 8.3, 14.1_

- [x] 8. Implement diagnostics, health, and trace propagation
  - Write failing tests for trace IDs, A-leg/B-leg diagnostics, and health output.
  - Implement structured logging helpers, health service, and attempt diagnostics service.
  - _Requirements: 13.1, 13.2, 13.3, 13.4, 13.5, 14.2_
  - _Boundary: lipcore/diag_
  - _Depends: 6.2, 7_

- [x] 9. Implement the frontend protocol plugins
  - **Emulator-first:** For each client-facing API flavor, deliver a **reference client emulator** (task 9.0.x) before the matching proxy `frontends/*` plugin (task 9.x). The emulator is the scriptable, spec-shaped client used in end-to-end integration tests; implementing agents must not treat the real proxy frontend as the first proof of wire compliance.
  - **Multimodal:** Every bundled frontend must correctly handle **multimodal** requests and responses on the v1 shared subset (e.g. images, PDFs); reference client emulators must support scripted multimodal scenarios for integration tests (see Requirement 15.7).
  - For each frontend plugin, write RED protocol contract tests before implementation, then implement decode/encode against the canonical model and event stream.
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 3.8, 5.2, 14.2, 15.1, 15.3, 15.7_
  - _Boundary: frontends/*, internal/testkit (reference emulators)_
  - _Depends: 2, 3, 4, 7_

- [x] 9.0 Build reference **client** emulators (official libraries, spec-faithful)
  - For each supported **frontend** protocol, add a scriptable **reference client** built on the **official vendor client library** for that API (not hand-rolled HTTP sketches). It issues requests and consumes responses/events exactly as a real client would per published API docs, so later E2E runs can point it at `lipstd` and assert compliance.
  - Include **multimodal** scenarios: at least one **image** and one **document (e.g. PDF)** flow per protocol where the official API supports that modality, so tests can verify proxy multimodal handling end-to-end.
  - Cross-check each emulator against the official specification (documented matrix: endpoints, auth headers, streaming modes, error shapes, idempotency where relevant). Do not start the matching proxy frontend plugin until this review is complete.
  - _Normative API URLs: `research.md` → **Official API specification references (normative docs)**._
  - _Requirements: 3.1–3.4, 15.2, 15.3, 15.7_
  - _Boundary: internal/testkit (or `cmd/` / `scripts/` wrappers that import official SDKs only—must not import `lipcore`)_
  - _Depends: 2_

- [x] 9.0.1 (P) Reference client emulator — OpenAI Responses API
  - Use the official OpenAI client library; cover streaming and non-streaming call patterns needed for integration tests.
  - Exercise **multimodal** message content (image + document paths per API capability).
  - _Requirements: 15.7_
  - _Boundary: testkit / runnable emulator package_
  - _Depends: 2_

- [x] 9.0.2 (P) Reference client emulator — legacy OpenAI-compatible (chat/completions) API
  - Use the official OpenAI client library (or the documented compatibility surface) for chat-style requests and responses used in tests.
  - Exercise **multimodal** inputs where the chat/completions surface supports them.
  - _Requirements: 15.7_
  - _Boundary: testkit / runnable emulator package_
  - _Depends: 2_

- [x] 9.0.3 (P) Reference client emulator — Anthropic Messages API
  - Use the official Anthropic client library; cover streaming and errors used in integration tests.
  - Exercise **multimodal** content blocks (image + document) per Messages API capabilities.
  - _Requirements: 15.7_
  - _Boundary: testkit / runnable emulator package_
  - _Depends: 2_

- [x] 9.0.4 (P) Reference client emulator — Gemini `generateContent` API
  - Use the official Google GenAI / Gemini client library for the supported request/stream patterns used in tests.
  - Exercise **multimodal** `Part` / file inputs (image + PDF or equivalent) per Gemini API capabilities.
  - _Requirements: 15.7_
  - _Boundary: testkit / runnable emulator package_
  - _Depends: 2_

- [x] 9.1 (P) Implement the OpenAI Responses frontend
  - Add streaming and non-streaming protocol goldens, including **multimodal** goldens for the v1 shared subset.
  - **Gate:** 9.0.1 completed and spec cross-check recorded; then implement the proxy adapter. _(Cross-check: `research.md` OpenAI Responses API reference; wire verified via `internal/refclient/openairesponses` + `internal/plugins/frontends/openairesponses` integration tests.)_
  - _Requirements: 3.1, 3.5, 3.6, 3.7, 3.8_
  - _Boundary: frontends/openairesponses_
  - _Depends: 9.0.1, 7_

- [x] 9.2 (P) Implement the legacy OpenAI-compatible frontend
  - Add chat-style request/response goldens and error-shape tests, including **multimodal** cases where supported.
  - **Gate:** 9.0.2 completed and spec cross-check recorded. _(Cross-check: `research.md` Chat Completions API reference; wire verified via `internal/refclient/openaichat` + `internal/plugins/frontends/openailegacy` integration tests.)_
  - _Requirements: 3.2, 3.5, 3.6, 3.7, 3.8_
  - _Boundary: frontends/openaicompat_
  - _Depends: 9.0.2, 7_

- [x] 9.3 (P) Implement the Anthropic Messages frontend
  - Add streaming mapping and protocol error-shape tests, including **multimodal** content blocks.
  - **Gate:** 9.0.3 completed and spec cross-check recorded.
  - _Requirements: 3.3, 3.5, 3.6, 3.7, 3.8_
  - _Boundary: frontends/anthropic_
  - _Depends: 9.0.3, 7_

- [x] 9.4 (P) Implement the Gemini generateContent frontend
  - Add request/stream mapping tests for the supported shared subset, including **multimodal** inputs and outputs.
  - **Gate:** 9.0.4 completed and spec cross-check recorded.
  - _Requirements: 3.4, 3.5, 3.6, 3.7, 3.8_
  - _Boundary: frontends/gemini_
  - _Depends: 9.0.4, 7_

- [ ] 10. Implement the backend protocol plugins
  - **Emulator-first:** For each **remote backend connector** protocol the proxy speaks as a client to the remote inference backends, deliver a **reference remote backend emulator** (task 10.0.x) before the matching `backends/*` connector. The emulator is a spec-faithful fake provider (scriptable, deterministic) used in E2E tests; do not use the first implementation of `backends/*` as the only validation of wire behavior.
  - **Multimodal:** Every bundled backend connector must correctly map **multimodal** canonical parts to provider APIs and back; reference backend emulators must accept and emit multimodal content for tests (see Requirement 15.8).
  - For each backend, write RED adapter tests; implement provider calls through official SDKs or official protocol definitions where available.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 4.7, 4.8, 5.1, 15.1, 15.2, 15.3, 15.8_
  - _Boundary: backends/*, internal/testkit (reference emulators)_
  - _Depends: 2, 3, 7_

- [ ] 10.0 Build reference **remote backend** emulators (official libraries, spec-faithful)
  - For each supported **backend connector** protocol, add a **reference server or stub service** that speaks the provider API on the wire as defined in official docs. Prefer **official server-side or SDK-hosted test doubles** where the vendor documents them; otherwise implement minimal HTTP/gRPC handlers using official request/response types from the vendor SDK so payloads stay spec-aligned.
  - Support **multimodal** request and response fixtures: at least one **image** and one **document** path per protocol where the API allows, so the proxy’s outbound multimodal mapping is testable without a live provider.
  - Cross-check each emulator against the official specification (streaming event sequences, error models, auth expectations). Do not start the matching proxy backend connector until this review is complete.
  - _Normative API URLs: `research.md` → **Official API specification references (normative docs)**._
  - _Requirements: 4.1–4.6, 15.2, 15.3, 15.8_
  - _Boundary: internal/testkit (or standalone test service packages that must not import `lipcore`)_
  - _Depends: 2_

- [x] 10.0.1 (P) Reference remote backend emulator — OpenAI Responses API
  - Expose the endpoints and event shapes the connector will call; use official OpenAI types/libraries where applicable.
  - Include **multimodal** request/response paths where the Responses API defines them.
  - _Requirements: 15.8_
  - _Boundary: testkit / runnable emulator_
  - _Depends: 2_

- [x] 10.0.2 (P) Reference remote backend emulator — legacy OpenAI-compatible API
  - Same as 10.0.1 for the chat/completions-compatible surface the backend plugin targets, including **multimodal** messages where supported.
  - _Requirements: 15.8_
  - _Boundary: testkit / runnable emulator_
  - _Depends: 2_

- [x] 10.0.3 (P) Reference remote backend emulator — Anthropic Messages API
  - Prefer patterns aligned with Anthropic’s official SDK/types for request/response and streaming, including **multimodal** content blocks.
  - _Requirements: 15.8_
  - _Boundary: testkit / runnable emulator_
  - _Depends: 2_

- [x] 10.0.4 (P) Reference remote backend emulator — Gemini `generateContent` API
  - Use official Google GenAI / Gemini server-side or types for faithful payloads and streams, including **multimodal** parts.
  - _Requirements: 15.8_
  - _Boundary: testkit / runnable emulator_
  - _Depends: 2_

- [x] 10.0.5 (P) Reference remote backend emulator — AWS Bedrock (Converse / ConverseStream)
  - Use AWS SDK types and documented event shapes; local or test-scoped endpoint acceptable if contractually equivalent. Include **multimodal** message content where Converse supports it.
  - _Requirements: 15.8_
  - _Boundary: testkit / runnable emulator_
  - _Depends: 2_

- [x] 10.0.6 (P) Reference local backend emulator — ACP subset
  - Match the ACP protocol surfaces the connector will use (session lifecycle, turns, progress, cancellation); use reference server libraries if the ecosystem provides them. Include **multimodal** or resource-reference paths **if and as** the v1 ACP subset exposes them per the official schema.
  - _Requirements: 15.8_
  - _Boundary: testkit / runnable emulator_
  - _Depends: 2_

- [ ] 10.0.7 Security Hardening of Reference remote backend emulators
  - Ensure all remote inference backend emulators created in tasks 10.0.1 to 10.0.6 are protected vs instance creation during runtime of the production code. This is to avoid accidental confusion of components. They should be only used / available to create instances during the test runs.

- [ ] 10.1 (P) Implement the OpenAI Responses backend
  - Add stub tests for streaming events and usage propagation, including **multimodal** mapping tests.
  - **Gate:** 10.0.1 completed and spec cross-check recorded.
  - _Requirements: 4.1, 4.7, 4.8, 5.1_
  - _Boundary: backends/openairesponses_
  - _Depends: 10.0.1, 7_

- [ ] 10.2 (P) Implement the legacy OpenAI-compatible backend
  - Add adapter tests proving canonical mapping and typed error classification, including **multimodal** messages where supported.
  - **Gate:** 10.0.2 completed and spec cross-check recorded.
  - _Requirements: 4.2, 4.7, 4.8, 5.1_
  - _Boundary: backends/openaicompat_
  - _Depends: 10.0.2, 7_

- [ ] 10.3 (P) Implement the Anthropic backend
  - Add adapter tests for message/tool/stream mapping on the shared subset, including **multimodal** content blocks.
  - **Gate:** 10.0.3 completed and spec cross-check recorded.
  - _Requirements: 4.3, 4.7, 4.8, 5.1_
  - _Boundary: backends/anthropic_
  - _Depends: 10.0.3, 7_

- [ ] 10.4 (P) Implement the Gemini backend
  - Add adapter tests for generateContent stream mapping on the shared subset, including **multimodal** parts.
  - **Gate:** 10.0.4 completed and spec cross-check recorded.
  - _Requirements: 4.4, 4.7, 4.8, 5.1_
  - _Boundary: backends/gemini_
  - _Depends: 10.0.4, 7_

- [ ] 10.5 (P) Implement the Bedrock backend
  - Add stub tests for Converse / ConverseStream event mapping and error handling, including **multimodal** Converse content where supported.
  - **Gate:** 10.0.5 completed and spec cross-check recorded.
  - _Requirements: 4.5, 4.7, 4.8, 5.1_
  - _Boundary: backends/bedrock_
  - _Depends: 10.0.5, 7_

- [ ] 10.6 (P) Implement the ACP backend subset
  - Add tests for initialization, session setup/reuse, prompt turn, progress notifications, and cancellation; add **multimodal**-adjacent tests if the v1 subset carries file or resource references per ACP schema.
  - **Gate:** 10.0.6 completed and spec cross-check recorded.
  - _Requirements: 4.6, 4.7, 4.8, 5.1, 8.5_
  - _Boundary: backends/acp_
  - _Depends: 10.0.6, 7_

- [ ] 11. Bundle the standard distribution and reference no-op hook plugins
  - Compose all mandatory plugins in `cmd/lipstd` and prove startup correctness.
  - Add no-op submit, part, and tool-reactor plugins to demonstrate extension seams without feature coupling.
  - _Requirements: 3.1-3.4, 4.1-4.6, 9.5, 10.5, 11.4, 12.1, 12.5_
  - _Boundary: cmd/lipstd, features/*_
  - _Depends: 9, 10_

- [ ] 11.1 (P) Add no-op submit and part hook reference plugins
  - Prove that the core works with hook plugins present but behaviorally inert.
  - _Requirements: 9.5, 10.5, 12.1_
  - _Boundary: features/submitnoop, features/partsnoop_
  - _Depends: 4.1, 4.2_

- [ ] 11.2 (P) Add the no-op tool-reactor reference plugin
  - Prove that the reserved tool-reactor path is active without policy logic.
  - _Requirements: 11.4, 11.5, 12.1_
  - _Boundary: features/toolreactornoop_
  - _Depends: 4.3_

- [ ] 12. Build the conformance matrix, migration fixtures, and release gates
  - Add cross-product tests for bundled frontends and backends on the shared subset, driven by **9.0.x reference clients** against the proxy and **10.0.x reference backends** behind the proxy (not ad-hoc mocks) wherever feasible.
  - This means all possible combinations of proxy front-end interfaces vs all combinations of back-end connector interfaces must have proper translation layer/service created, example:
    - Responses API (proxy front-end) <-> Responses API backend
    - Responses API (proxy front-end) <-> Legacy OpenAI Chat Completions backend
    - Responses API (proxy front-end) <-> Anthropic Messages API backend
    - Responses API (proxy front-end) <-> Gemini API backend
    - Responses API (proxy front-end) <-> ACP Agent Client Protocol pseudo backend
  - Include **multimodal** matrix rows (at least one viable frontend/backend pair with image + document-style content per Requirement 15.9).
  - Import or derive Python-repo fixtures/goldens where practical.
  - Gate readiness on conformance, race, and critical fuzz targets.
  - _Requirements: 15.2, 15.3, 15.4, 15.5, 15.6, 15.9, 14.6_
  - _Boundary: internal/testkit, all protocol plugins_
  - _Depends: 9, 10, 11_
