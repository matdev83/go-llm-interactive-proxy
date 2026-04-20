# Implementation Plan

## Task List

- [ ] 1. Establish the repository skeleton, package boundaries, and TDD harness
  - Create the Go module, package roots, and composition root layout defined in the File Structure Plan.
  - Add baseline CI commands for `go test`, fuzz targets, and `-race`.
  - Write failing boundary tests that prove `lipcore` does not import bundled frontend, backend, or feature-plugin packages.
  - _Requirements: 1.1, 1.2, 1.5, 14.3, 14.5, 15.1_
  - _Boundary: cmd/lipstd, lipapi, lipsdk, lipcore, internal/testkit_

- [ ] 1.1 Create the core package scaffolding and compile-only contracts
  - Write RED tests for package visibility, constructor wiring, and typed startup errors.
  - Add empty but compiling packages for `lipapi`, `lipsdk`, and `lipcore`.
  - _Requirements: 1.1, 1.2, 12.6, 14.4, 14.5, 15.1_

- [ ] 1.2 (P) Build the shared test kit and CI execution profile
  - Add provider stub scaffolding, golden helpers, and canonical event assertions.
  - Add race, fuzz, and benchmark entrypoints to CI or documented local commands.
  - _Requirements: 15.2, 15.3, 15.4, 14.6_
  - _Boundary: internal/testkit, CI_
  - _Depends: 1.1_

- [ ] 2. Define the canonical call, event, capability, and error model
  - Write failing contract tests that lock the shared request subset, event ordering, and collected non-streaming behavior.
  - Implement the minimal canonical types and typed error hierarchy in `lipapi`.
  - _Requirements: 2.1, 2.2, 2.3, 2.5, 5.2, 14.4, 15.1_
  - _Boundary: lipapi_

- [ ] 2.1 Implement canonical request and part types
  - Define messages, parts, tool declarations, generation options, and vendor extensions.
  - Add validation tests for invariants and unsupported combinations.
  - _Requirements: 2.1, 2.3, 10.4, 14.4_

- [ ] 2.2 Implement canonical event types and the collector contract
  - Define stream event families and collection rules for non-streaming responses.
  - Add RED/GREEN tests for event ordering, collection, and error termination.
  - _Requirements: 2.2, 5.1, 5.2, 11.1_

- [ ] 2.3 Implement capability declarations and typed capability errors
  - Define the capability vocabulary and negotiation result types.
  - Add tests for lossless, downgrade, and reject outcomes.
  - _Requirements: 2.4, 4.7, 12.2, 12.5_

- [ ] 3. Build the stable plugin SDK and explicit registration model
  - Write failing tests for duplicate plugin IDs, missing mandatory plugins, and opaque plugin config handling.
  - Implement registration contracts for frontend, backend, submit-hook, part-hook, and tool-reactor plugins.
  - _Requirements: 1.3, 12.1, 12.2, 12.3, 12.5, 12.6, 14.5, 15.1_
  - _Boundary: lipsdk, lipcore/runtime_

- [ ] 3.1 Implement plugin registration and lifecycle contracts
  - Add startup validation for uniqueness and mandatory bundle coverage.
  - Keep constructor wiring explicit in the composition root.
  - _Requirements: 12.1, 12.2, 12.5, 12.6_

- [ ] 3.2 Implement opaque plugin config payload handling
  - Add config tests proving the core never needs plugin-private schema knowledge.
  - Pass raw config payloads through the plugin factory surface.
  - _Requirements: 12.3, 12.4, 14.4_

- [ ] 4. Implement the stream engine, cancellation, and hook bus
  - Write failing tests for streaming pass-through, cancellation propagation, hook ordering, mutation validation, and fail-open/fail-closed behavior.
  - Implement `EventStream`, collector helpers, cancellation propagation, and the hook bus.
  - _Requirements: 5.1, 5.2, 5.3, 9.1, 9.4, 10.1, 10.2, 10.3, 11.3, 11.5, 14.1, 14.3_
  - _Boundary: lipcore/stream, lipcore/hooks_

- [ ] 4.1 Implement submit hook execution
  - Add typed metadata, ordering, and rejection handling.
  - Prove via tests that the runtime works with zero registered hooks.
  - _Requirements: 9.1, 9.2, 9.3, 9.5_

- [ ] 4.2 (P) Implement request and response part hook surfaces
  - Add canonical mutation validation and typed hook errors.
  - Add RED tests for invalid part rewrites and safe pass-through behavior.
  - _Requirements: 10.1, 10.2, 10.3, 10.4, 10.5_
  - _Boundary: lipcore/hooks, lipsdk/hooks_
  - _Depends: 2.1, 2.2_

- [ ] 4.3 (P) Implement reserved tool-reactor orchestration surfaces
  - Add canonical tool event types, reactor decisions, and fail-open default behavior.
  - Add tests for pass-through, swallow, rewrite, and replace decisions at the interface level.
  - _Requirements: 11.1, 11.2, 11.3, 11.4, 11.5_
  - _Boundary: lipcore/hooks, lipsdk/hooks_
  - _Depends: 2.2_

- [ ] 5. Implement the route selector parser, planner, and session-aware weighted routing
  - Write RED tests for explicit selectors, ordered failover, weighted routing, first-request annotations, invalid annotation combinations, and candidate exclusion after failure.
  - Implement the selector AST, parser, and planner.
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 7.1, 7.2, 7.3, 7.4, 14.4, 15.4_
  - _Boundary: lipcore/routing_

- [ ] 5.1 Implement ordered failover planning and candidate exclusions
  - Add tests proving left-to-right failover and deterministic exclusion behavior.
  - _Requirements: 6.2, 6.4, 6.5_

- [ ] 5.2 Implement weighted routing and first-request session rules
  - Add tests for single `[first]` enforcement, first-request consumption, and retry-path behavior.
  - _Requirements: 6.3, 7.1, 7.2, 7.3, 7.4, 7.5_

- [ ] 6. Implement the B2BUA store, lineage model, and recovery policy
  - Write failing tests for A-leg resolution, B-leg allocation, continuity reuse, attempt recording, and in-memory expiration.
  - Implement the in-memory B2BUA store and typed lineage records.
  - _Requirements: 7.5, 8.1, 8.2, 8.4, 8.5, 8.6, 13.1, 13.2, 14.3, 14.6_
  - _Boundary: lipcore/b2bua_

- [ ] 6.1 Implement A-leg continuity and first-request state retention
  - Add tests covering explicit continuity keys and safe new-session fallback.
  - _Requirements: 7.5, 8.1, 8.5, 8.6_

- [ ] 6.2 Implement B-leg sequence allocation and attempt record queries
  - Add tests for monotonic sequencing, lineage reads, and surfaced/swallowed outcomes.
  - _Requirements: 8.2, 8.4, 13.2_

- [ ] 7. Implement the core execution engine with pre-output recovery semantics
  - Write failing end-to-end executor tests for normal success, recoverable pre-output failure, post-output failure, cancellation, and collector-based non-streaming responses.
  - Implement the execution engine that combines hooks, capability checks, route planning, B2BUA, and backend invocation.
  - _Requirements: 5.1, 5.3, 5.4, 5.5, 6.4, 6.6, 8.3, 8.4, 9.1, 10.2, 11.1, 13.1, 13.3, 14.1_
  - _Boundary: lipcore/runtime_
  - _Depends: 2, 4, 5, 6_

- [ ] 7.1 Implement capability negotiation in the executor path
  - Reject unsupported combinations before upstream calls begin.
  - _Requirements: 2.4, 4.7, 12.2_

- [ ] 7.2 Implement recoverable pre-output failure swallowing and retry orchestration
  - Add tests that one client request may create multiple related backend attempts while surfacing one logical response.
  - _Requirements: 5.4, 5.5, 6.4, 8.3, 8.4_

- [ ] 7.3 Implement post-output failover prohibition and cancellation propagation
  - Add tests proving no silent recovery after visible output has started.
  - _Requirements: 5.3, 5.4, 8.3, 14.1_

- [ ] 8. Implement diagnostics, health, and trace propagation
  - Write failing tests for trace IDs, A-leg/B-leg diagnostics, and health output.
  - Implement structured logging helpers, health service, and attempt diagnostics service.
  - _Requirements: 13.1, 13.2, 13.3, 13.4, 13.5, 14.2_
  - _Boundary: lipcore/diag_
  - _Depends: 6.2, 7_

- [ ] 9. Implement the frontend protocol plugins
  - For each frontend, write RED protocol contract tests before implementation.
  - Implement decode/encode behavior against the canonical model and event stream.
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 5.2, 14.2, 15.1, 15.3_
  - _Boundary: frontends/*_
  - _Depends: 2, 3, 4, 7_

- [ ] 9.1 (P) Implement the OpenAI Responses frontend
  - Add streaming and non-streaming protocol goldens.
  - _Requirements: 3.1, 3.5, 3.6_
  - _Boundary: frontends/openairesponses_
  - _Depends: 7_

- [ ] 9.2 (P) Implement the legacy OpenAI-compatible frontend
  - Add chat-style request/response goldens and error-shape tests.
  - _Requirements: 3.2, 3.5, 3.6_
  - _Boundary: frontends/openaicompat_
  - _Depends: 7_

- [ ] 9.3 (P) Implement the Anthropic Messages frontend
  - Add streaming mapping and protocol error-shape tests.
  - _Requirements: 3.3, 3.5, 3.6_
  - _Boundary: frontends/anthropic_
  - _Depends: 7_

- [ ] 9.4 (P) Implement the Gemini generateContent frontend
  - Add request/stream mapping tests for the supported shared subset.
  - _Requirements: 3.4, 3.5, 3.6_
  - _Boundary: frontends/gemini_
  - _Depends: 7_

- [ ] 10. Implement the backend protocol plugins
  - For each backend, write RED adapter and stub-provider tests before implementation.
  - Implement provider calls through the official SDKs or official protocol definitions where available.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 4.7, 5.1, 15.1, 15.2, 15.3_
  - _Boundary: backends/*_
  - _Depends: 2, 3, 7_

- [ ] 10.1 (P) Implement the OpenAI Responses backend
  - Add stub tests for streaming events and usage propagation.
  - _Requirements: 4.1, 4.7, 5.1_
  - _Boundary: backends/openairesponses_
  - _Depends: 7_

- [ ] 10.2 (P) Implement the legacy OpenAI-compatible backend
  - Add adapter tests proving canonical mapping and typed error classification.
  - _Requirements: 4.2, 4.7, 5.1_
  - _Boundary: backends/openaicompat_
  - _Depends: 7_

- [ ] 10.3 (P) Implement the Anthropic backend
  - Add adapter tests for message/tool/stream mapping on the shared subset.
  - _Requirements: 4.3, 4.7, 5.1_
  - _Boundary: backends/anthropic_
  - _Depends: 7_

- [ ] 10.4 (P) Implement the Gemini backend
  - Add adapter tests for generateContent stream mapping on the shared subset.
  - _Requirements: 4.4, 4.7, 5.1_
  - _Boundary: backends/gemini_
  - _Depends: 7_

- [ ] 10.5 (P) Implement the Bedrock backend
  - Add stub tests for Converse / ConverseStream event mapping and error handling.
  - _Requirements: 4.5, 4.7, 5.1_
  - _Boundary: backends/bedrock_
  - _Depends: 7_

- [ ] 10.6 (P) Implement the ACP backend subset
  - Add tests for initialization, session setup/reuse, prompt turn, progress notifications, and cancellation.
  - _Requirements: 4.6, 4.7, 5.1, 8.5_
  - _Boundary: backends/acp_
  - _Depends: 7_

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
  - Add cross-product tests for bundled frontends and backends on the shared subset.
  - Import or derive Python-repo fixtures/goldens where practical.
  - Gate readiness on conformance, race, and critical fuzz targets.
  - _Requirements: 15.2, 15.3, 15.4, 15.5, 15.6, 14.6_
  - _Boundary: internal/testkit, all protocol plugins_
  - _Depends: 9, 10, 11_
