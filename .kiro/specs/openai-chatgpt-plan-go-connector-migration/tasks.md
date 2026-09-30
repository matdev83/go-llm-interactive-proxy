# Implementation Plan

## 1. Establish the independent connector boundary

- [ ] 1.1 Add the independently buildable `openai-chatgpt-plan` connector module
  - Create the connector module, plugin command, service skeleton, manifest template, release metadata and parity suite without importing `connectors/codex/internal` or root `internal` packages.
  - Export the direct inference factory `openai-chatgpt-plan`; reserve the app-server factory for Phase 7.
  - Observable completion: the backend-plugin host discovers/configures the new factory and `GOWORK=off` build/test succeeds without a root-module dependency on the connector.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.7, 14.7_
  - _Boundary: backend plugin / composition root_
  - _Depends: none_
  - _Validation: GOWORK=off go test ./... (new module) && make quality-checks_

- [ ] 1.2 Add coexistence staging without changing legacy behavior (P)
  - Add testkit/manifest staging for the new connector while leaving existing Codex staging untouched.
  - Observable completion: tests discover old and new connector factories in one plugin root with distinct ids and no aliasing.
  - _Requirements: 1.6, 12.1_
  - _Boundary: tests / config wiring_
  - _Depends: 1.1_
  - _Validation: go test ./internal/testkit/backendplugin/..._

## 2. Build SIWC host/profile storage and authorization

- [ ] 2.1 Implement stable host-id persistence
  - Create/load a protected host record and atomic first-start path.
  - Prefer a JWK-thumbprint URI if safe/practical; otherwise use a persisted UUID URI.
  - Observable completion: restart and concurrent-first-start tests resolve one stable, non-PII host id; profile import cannot overwrite it.
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6_
  - _Boundary: backend plugin / state adapter_
  - _Depends: 1.1_
  - _Validation: go test -race ./internal/siwc/... (new module)_

- [ ] 2.2 Define the SIWC profile record and protected atomic profile store
  - Persist validated subject, issued client id, granted scopes, token set, expiry, optional retained identity hints, local label and reauth status.
  - Reuse `oauthcred` protected-file/redaction primitives where clean; do not widen its generic token schema with OpenAI-only fields.
  - Observable completion: round-trip/version/permissions/symlink/corruption/atomic-replace tests pass.
  - _Requirements: 2.11, 2.12, 3.6, 14.1_
  - _Boundary: backend plugin / state adapter_
  - _Depends: 2.1_
  - _Validation: go test ./internal/siwc/..._

- [ ] 2.3 Implement dynamic first registration and returning-account authorization
  - Generate state/nonce/PKCE, select one loopback `redirect_uri`, run the callback listener, use `dynamic_agent_client` only for first registration, send stable host id and product agent name, and retain the returned issued client id.
  - Carry the exact selected `redirect_uri` through the pending authorization state and reuse it byte-for-byte during code exchange; never reconstruct or substitute scheme, host, port, or path.
  - Returning authorization uses the saved issued client id and validates subject before credential replacement.
  - Observable completion: fake auth integration proves exact parameters and exact callback-URI reuse; state mismatch/cancellation/timeout/missing issued-client paths fail safely; a mutated/reconstructed exchange URI is rejected by the test server; `dynamic_agent_client` is never stored as the issued id.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.8, 2.14_
  - _Boundary: backend plugin / driving adapter + app orchestration_
  - _Depends: 2.2_
  - _Validation: go test ./internal/siwc/... ./cmd/lip-backend-openaichatgptplan/..._

- [ ] 2.4 Implement OIDC/JWKS and granted-scope validation
  - Validate signature, issuer, audience, expiry, nonce and stable subject; require direct plan-use scope before profile activation.
  - Bound/cache JWKS safely and reject unknown key/algorithm.
  - Observable completion: positive/negative token fixtures pass without raw-token logging.
  - _Requirements: 2.6, 2.7, 2.8, 2.12, 14.1, 14.6_
  - _Boundary: backend plugin / domain policy + driven adapter_
  - _Depends: 2.3_
  - _Validation: go test ./internal/siwc/..._

- [ ] 2.5 Implement serialized rotating refresh, explicit profile selection and logout
  - Refresh with issued client id/resource; persist replacement refresh token atomically; classify terminal vs transient failures.
  - Provide list/select/add/reauthorize/logout operations; do not quota-rotate across registrations.
  - Logout resolves the OIDC discovery `revocation_endpoint`, attempts form-encoded refresh-token revocation with the issued client id, retries network/5xx failures with bounded backoff while the refresh token is retained, then distinguishes confirmed remote revocation from local-only sign-out when confirmation cannot be obtained.
  - Observable completion: concurrent refresh calls provider once per profile; terminal rotating-token failure requires reauth; profile switching is explicit; logout tests cover HTTP 200 confirmation, already-invalid token success semantics, retryable failure, and local completion with an explicit unconfirmed-revocation result.
  - _Requirements: 2.9, 2.10, 2.13, 2.14, 8.3, 8.5_
  - _Boundary: backend plugin / app orchestration + state adapter_
  - _Depends: 2.4_
  - _Validation: go test -race ./internal/siwc/..._

## 3. Implement account-specific public model inventory

- [ ] 3.1 Add the authenticated public `/v1/models` client and parser
  - Authenticate with the selected profile token, retain list-visible rows, preserve slug/display metadata and bound response bodies.
  - Observable completion: reference-server tests prove exact public path/header and visibility filtering.
  - _Requirements: 7.1, 7.2, 7.3, 7.7, 14.2_
  - _Boundary: backend plugin / driven adapter_
  - _Depends: 2.5_
  - _Validation: go test ./internal/service/... ./internal/siwc/..._

- [ ] 3.2 Make inventory profile-aware and eliminate Codex catalog assumptions from the new module
  - Key/invalidate cache by registration identity; never use another profile's catalog.
  - Do not invoke `codex debug models`, Codex binary resolution, client-version gates, private catalog parsing or old shipped snapshots.
  - Observable completion: two-profile tests prove isolation and static architecture checks find no old catalog dependency in the new module.
  - _Requirements: 7.4, 7.5, 7.6_
  - _Boundary: backend plugin / query seam_
  - _Depends: 3.1_
  - _Validation: go test ./internal/service/... + architecture grep/test_

## 4. Implement SIWC Responses projection and preview policy

- [ ] 4.1 Add failing canonical-to-wire fixtures before implementation
  - Cover instructions, system/developer/user/assistant history, function calls/results, multimodal input, tool choice, reasoning option, and downstream streaming/non-streaming modes.
  - Assert absence of Codex prompt fallback, `<user_instructions>`, private headers and private endpoints.
  - Observable completion: fixtures define the target wire contract and fail against an unimplemented projector.
  - _Requirements: 4.8, 4.9, 5.1, 5.2, 5.3, 5.4, 5.6, 6.5, 6.6, 6.8_
  - _Boundary: backend plugin / tests_
  - _Depends: 1.1_
  - _Validation: go test ./internal/responses/..._

- [ ] 4.2 Implement deterministic instruction/input/tool projection
  - Map `Call.Instructions` to `instructions`; fold explicit system conversation text into legal instruction representation; preserve legal developer/user/assistant/tool chronology and call ids.
  - Preserve supported image/file inputs.
  - Observable completion: fixtures pass and generated JSON contains no Codex identity prompt/glue.
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 6.5, 6.6_
  - _Boundary: backend plugin / driven adapter_
  - _Depends: 4.1_
  - _Validation: go test ./internal/responses/..._

- [ ] 4.3 Implement the centralized SIWC preview request policy
  - Force `store:false` and `stream:true`; reject or deliberately omit every documented unsupported field according to explicit canonical semantics.
  - Reject unsupported hosted tools and HTTP `previous_response_id` before network execution.
  - Observable completion: table tests cover every documented unsupported field/tool plus positive minimal/tool/multimodal requests.
  - _Requirements: 4.3, 6.1, 6.2, 6.3, 6.4, 6.7, 6.8, 6.9_
  - _Boundary: backend plugin / domain policy_
  - _Depends: 4.2_
  - _Validation: go test ./internal/responses/..._

## 5. Implement public stream-only Responses execution

- [ ] 5.1 Implement public `/v1/responses` HTTP/SSE client
  - Use selected profile bearer token and public base URL; always request upstream streaming; bound request/error/SSE bodies and preserve cancellation.
  - Observable completion: reference server observes only the public endpoint and standard bearer auth, for both downstream delivery modes.
  - _Requirements: 4.1, 4.2, 4.3, 4.6, 4.8, 4.9_
  - _Boundary: backend plugin / driven adapter_
  - _Depends: 4.3, 2.5_
  - _Validation: go test ./internal/responses/..._

- [ ] 5.2 Normalize Responses SSE into canonical events and require terminal completion
  - Map text/tool/reasoning/usage/completion/failure events; EOF without terminal is failure.
  - Preserve no-retry-after-visible-output semantics through the managed stream.
  - Observable completion: fixtures prove `response.completed` is the only success terminal and committed output cannot be replayed transparently.
  - _Requirements: 4.4, 4.5, 4.7, 6.5, 8.1, 14.2_
  - _Boundary: backend plugin / driven adapter_
  - _Depends: 5.1_
  - _Validation: go test ./internal/responses/..._

- [ ] 5.3 Wire the direct factory Execute path and downstream non-stream collection
  - Use one streaming provider path for both downstream modes; do not add a provider non-stream request path.
  - Verify cancellation and accounting sideband integration.
  - Observable completion: host integration returns a downstream non-stream result while provider capture proves upstream `stream:true`.
  - _Requirements: 4.3, 4.6, 4.7, 8.7_
  - _Boundary: backend plugin / app orchestration_
  - _Depends: 5.2_
  - _Validation: go test ./... (new module) + targeted backendplugin host integration_

## 6. Implement SIWC error and usage semantics

- [ ] 6.1 Add structured admission/stream error classification
  - Preserve status/code/request id and bounded diagnostics; classify unsupported capability, route, invalid user/context, temporary unavailable, usage exhausted and revocation/refresh outcomes.
  - Observable completion: provider fixtures produce deterministic typed errors without secret leakage.
  - _Requirements: 8.1, 8.2, 8.4, 8.5_
  - _Boundary: backend plugin / domain policy_
  - _Depends: 5.2_
  - _Validation: go test ./internal/responses/... ./internal/siwc/..._

- [ ] 6.2 Prove no automatic cross-profile/model recovery
  - Usage exhaustion terminates selected profile; remove any temptation to reuse PlanTypeHint/GPT downgrade/x-codex recovery semantics.
  - Observable completion: usage-limit fixtures produce one selected-profile provider attempt and no silent model/profile substitution.
  - _Requirements: 2.13, 8.3, 8.6_
  - _Boundary: backend plugin / app orchestration_
  - _Depends: 6.1_
  - _Validation: go test ./internal/service/..._

- [ ] 6.3 Integrate completed-stream usage with existing accounting evidence
  - Emit provider usage once through current sideband contracts; do not invent API-dollar price/rating for subscription-funded usage.
  - Observable completion: accounting tests receive one deduplicated usage evidence record with non-secret local profile provenance.
  - _Requirements: 8.7, 8.8_
  - _Boundary: backend plugin / accounting sideband_
  - _Depends: 5.2_
  - _Validation: go test ./... (new module) + accounting contract tests_

## 7. Add official SIWC-backed Codex app-server

- [ ] 7.1 Add failing exact launch/provider tests first
  - Pin public base URL, token env key, Responses wire API, `requires_openai_auth=false`, `supports_websockets=false`, truthful clientInfo name/title/version and secret-safe environment.
  - Observable completion: tests define official SIWC app-server launch and fail against legacy assumptions.
  - _Requirements: 9.1, 9.2, 9.3, 9.4, 9.8_
  - _Boundary: backend plugin / tests_
  - _Depends: 2.5_
  - _Validation: go test ./internal/appserver/..._

- [ ] 7.2 Implement `openai-chatgpt-plan-app-server`
  - Reuse selected SIWC token; launch Codex with documented public Responses provider; preserve agent-runtime execution class/workspace/process lifecycle.
  - Observable completion: fake app-server receives exact provider config and factory descriptor remains `agent_runtime`.
  - _Requirements: 9.1, 9.2, 9.3, 9.4, 9.8_
  - _Boundary: backend plugin / agent-runtime driven adapter_
  - _Depends: 7.1_
  - _Validation: go test ./internal/appserver/... ./internal/service/..._

- [ ] 7.3 Implement token-renewal child restart and thread resume
  - On replacement access token, restart child, initialize and `thread/resume` saved thread through documented RPC.
  - Observable completion: fake multi-turn app-server integration continues across token rollover without elevating thread id into core authority.
  - _Requirements: 9.5, 9.6, 9.7_
  - _Boundary: backend plugin / app orchestration_
  - _Depends: 7.2_
  - _Validation: go test -race ./internal/appserver/..._

## 8. Revalidate reasoning/context without private Codex compaction

- [ ] 8.1 Prove full-history SIWC HTTP correctness without private continuation
  - Multi-turn canonical calls replay required history, tool trajectories and long context; HTTP never sends `previous_response_id`.
  - Observable completion: correctness survives connector restart because no provider response-id/checkpoint state is required.
  - _Requirements: 6.3, 10.1, 10.2, 10.6_
  - _Boundary: backend plugin / tests_
  - _Depends: 5.3_
  - _Validation: go test ./internal/responses/..._

- [ ] 8.2 Revalidate exact reasoning replay as an optional public capability
  - Add deterministic fixtures and an opt-in live probe for exact Responses reasoning items/encrypted content.
  - If unsupported, advertise reduced replay support and remain on public canonical history; no private fallback.
  - Observable completion: result is explicit in capabilities/tests and neither branch invokes private endpoint.
  - _Requirements: 10.4, 10.5_
  - _Boundary: backend plugin / capability negotiation + tests_
  - _Depends: 8.1_
  - _Validation: go test ./... (new module) + environment-gated live probe_

- [ ] 8.3 Keep generic context compaction outside the SIWC provider adapter
  - Revalidate existing canonical context features where semantically valid; do not implement `/responses/compact` or advertise `codex.responses.compaction.v2`.
  - Observable completion: descriptors/parity tests expose no private Codex compaction dialect on the new connector.
  - _Requirements: 10.1, 10.3, 10.7_
  - _Boundary: backend plugin / capability negotiation_
  - _Depends: 8.1_
  - _Validation: go test ./internal/service/... && make parity-checks_

## 9. Reconcile coding-harness compatibility

- [ ] 9.1 Prove baseline harness-shaped requests with `codex-client-compat` disabled
  - Exercise OpenCode-shaped and a materially different harness-shaped request through frontend→canonical→new connector, including tools.
  - Observable completion: both complete canonical tool-capable streams without client-name prompt bridges.
  - _Requirements: 5.7, 11.1, 11.2_
  - _Boundary: tests / feature boundary_
  - _Depends: 5.3_
  - _Validation: targeted frontend + backendplugin integration tests_

- [ ] 9.2 Classify every current `codex-client-compat` behavior
  - Produce a test-backed keep/generalize/remove matrix; never copy `openai_codex.*` keys into the new backend.
  - Move only truly provider-neutral fixes to generic seams.
  - Observable completion: every current bridge/extension has one explicit disposition backed by tests or live evidence.
  - _Requirements: 11.2, 11.3, 11.4, 11.5, 11.6_
  - _Boundary: feature plugin / research-to-code reconciliation_
  - _Depends: 9.1_
  - _Validation: go test ./internal/plugins/features/codexclientcompat/... + targeted frontend tests_

## 10. Automated conformance gate for the new connector

- [ ] 10.1 Certify SIWC auth/profile security and races
  - Run positive/negative auth, permissions, refresh, race and fuzz tests; scan logs/captures for credential leakage.
  - Observable completion: no races, no leaked token material, deterministic failure classes.
  - _Requirements: 2.4, 2.6, 2.9, 2.10, 2.12, 14.1, 14.5, 14.6_
  - _Boundary: tests / security_
  - _Depends: 2.5_
  - _Validation: go test -race ./internal/siwc/... + targeted fuzz runs_

- [ ] 10.2 Certify direct Responses/inventory wire contracts
  - Run exact-path/header/body/SSE/model-list tests and negative assertions for private Codex fields; include downstream non-stream/upstream stream.
  - Observable completion: test server observes only auth endpoints, `/v1/models`, and `/v1/responses` for the direct connector.
  - _Requirements: 4.1, 4.3, 4.8, 5.1, 6.1, 7.1, 14.2, 14.3_
  - _Boundary: tests / contract_
  - _Depends: 3.2, 5.3, 6.3_
  - _Validation: go test ./... (new module)_

- [ ] 10.3 Certify plugin ABI, app-server and architecture boundaries
  - Stage the real executable through backend-plugin host tests; run app-server restart/resume and import/dependency checks.
  - Observable completion: both factories negotiate correctly, module builds independently, root/core boundary stays clean.
  - _Requirements: 1.1, 1.3, 1.4, 9.8, 14.4, 14.7_
  - _Boundary: tests / architecture_
  - _Depends: 7.3_
  - _Validation: GOWORK=off go test ./... (new module) && make quality-checks && make parity-checks_

## 11. Mandatory human empirical acceptance gate

- [ ] 11.1 Complete a real SIWC registration/profile lifecycle
  - Use an eligible Plus/Pro account and record only non-secret evidence; verify issued client id, direct scope, stable host id, restart persistence and real/safely accelerated refresh.
  - Observable completion: human records PASS tied to tested commit/build, OS and plan class.
  - _Requirements: 12.1, 12.2, 12.11_
  - _Boundary: human / live acceptance_
  - _Depends: 10.1, 10.2, 10.3_
  - _Validation: operator evidence_

- [ ] 11.2 Validate public inventory and direct inference in real use
  - List account models; complete downstream streaming and non-stream requests; capture sanitized evidence for `store:false`, `stream:true`, public endpoints and absence of private Codex headers/prompt.
  - Observable completion: both delivery modes reach `response.completed` through public `/v1/responses`.
  - _Requirements: 12.3, 12.4, 12.8_
  - _Boundary: human / live acceptance_
  - _Depends: 11.1_
  - _Validation: operator evidence + sanitized wire capture_

- [ ] 11.3 Validate real coding harnesses and client-side tool loops
  - Run OpenCode and one materially different harness; execute a function/custom tool and multi-turn continuation; keep `codex-client-compat` disabled for baseline.
  - Observable completion: both harnesses complete useful multi-turn/tool tasks without Codex identity prompt injection.
  - _Requirements: 5.8, 11.7, 12.5, 12.6, 12.7_
  - _Boundary: human / live acceptance_
  - _Depends: 11.2_
  - _Validation: operator evidence_

- [ ] 11.4 Validate quota/error behavior and the SIWC app-server
  - Exercise a controlled usage-limit/error condition or closest deterministic supported equivalent; confirm no silent profile rotation.
  - Because this specification retains `openai-chatgpt-plan-app-server` in the supported end state, run a live SIWC app-server session that covers token renewal, child restart with the replacement token, re-initialization, and `thread/resume` of the saved thread id.
  - Observable completion: human records PASS for both the controlled quota/error semantics and the full app-server token-renewal/restart/thread-resume path; any app-server failure blocks Task 11.5 while that factory remains in the supported end state.
  - _Requirements: 12.9, 12.10_
  - _Boundary: human / live acceptance_
  - _Depends: 11.3_
  - _Validation: operator evidence_

- [ ] 11.5 Record explicit migration approval
  - Aggregate Tasks 11.1–11.4 into a concise non-secret acceptance record tied to tested commit.
  - Do not mark this task complete unless Task 11.4 includes a passing live token-renewal/restart/thread-resume result for `openai-chatgpt-plan-app-server`; if the project instead decides not to support that factory, requirements/design/tasks must first be revised coherently to remove it from the migration end state.
  - This checkbox is the hard dependency for every destructive task below.
  - Observable completion: project owner/human operator marks the migration gate PASS.
  - _Requirements: 12.1, 12.11_
  - _Boundary: release governance_
  - _Depends: 11.1, 11.2, 11.3, 11.4_
  - _Validation: explicit human approval_

## 12. Cut over operator-facing configuration after the gate

- [ ] 12.1 Make the new connector the documented ChatGPT-plan route
  - Update docs/examples/install/release guidance to `openai-chatgpt-plan`; distinguish direct inference vs app-server; document preview restrictions and OSS/local-hosted scope.
  - Observable completion: current operator docs no longer recommend `~/.codex/auth.json` or private `backend-api` for subscription inference.
  - _Requirements: 13.1, 14.9, 14.10_
  - _Boundary: docs / config wiring_
  - _Depends: 11.5_
  - _Validation: docs/config tests + grep_

- [ ] 12.2 Reconcile `codex-client-compat` using automated/live evidence
  - Remove private-Codex prompt bridges/extensions and backend targeting no longer needed; preserve only behavior justified by Task 9.2/11 evidence.
  - Observable completion: OpenCode + second-harness regression tests pass on the new connector with no `openai_codex.*` dependency.
  - _Requirements: 11.3, 11.4, 11.5, 11.6, 11.7_
  - _Boundary: feature plugin / tests_
  - _Depends: 11.5, 9.2_
  - _Validation: go test ./internal/plugins/features/codexclientcompat/... + targeted integration_

## 13. Remove the legacy private Codex implementation

- [ ] 13.1 Remove `openai-codex` private HTTP factory and transport
  - Delete factory/export/manifest/release/config paths plus private backend-api HTTP/WS, Codex CLI OAuth refresh, private headers, conversation/session transport affinity and default Codex identity instruction.
  - Observable completion: no production code can construct or route to the legacy direct factory.
  - _Requirements: 13.1, 13.3, 13.8_
  - _Boundary: backend plugin_
  - _Depends: 11.5, 12.1_
  - _Validation: go test ./connectors/codex/... during deletion + repository grep_

- [ ] 13.2 Remove obsolete account-pool, downgrade, quota and verbosity-turn code
  - Delete unreachable managed-Codex pool, plan hints/model downgrade, quota-header storage, session affinity/turn bumps and stale config/docs; prune dependencies used only there.
  - Observable completion: no supported factory references these types/fields and static/dead-code checks are clean.
  - _Requirements: 13.4, 13.9_
  - _Boundary: backend plugin / docs_
  - _Depends: 13.1_
  - _Validation: go test ./connectors/codex/... && make quality-checks_

- [ ] 13.3 Remove private native Codex context/compaction machinery
  - Delete `/responses/compact`, `codex.responses.compaction.v2`, private checkpoint/continuation state and compaction accounting sideband when no supported factory uses them.
  - Keep archived historical specs as evidence, not production obligations.
  - Observable completion: no supported descriptor/call site exposes the private compaction dialect.
  - _Requirements: 10.1, 10.3, 10.7, 13.6_
  - _Boundary: backend plugin / tests_
  - _Depends: 13.1, 8.3_
  - _Validation: repository grep + make parity-checks_

- [ ] 13.4 Replace/remove legacy `openai-codex-app-server`
  - Remove old factory id after the SIWC app-server is proven; reuse/delete shared ACP helpers according to remaining callers; keep no permanent old-auth alias.
  - Observable completion: only the SIWC-backed app-server factory is production-visible.
  - _Requirements: 13.2, 13.5_
  - _Boundary: backend plugin / manifest_
  - _Depends: 11.5, 7.3, 12.1_
  - _Validation: connector parity suite + manifest/release gate_

- [ ] 13.5 Remove or archive the old `connectors/codex` module
  - If no supported production factory remains, delete module/command/manifest/release metadata/replaces/packaging. Move any genuinely generic helper to the correct support module first rather than keeping a mostly dead connector.
  - Observable completion: package/release inventory contains only the new SIWC connector for ChatGPT-plan/Codex-plan integration.
  - _Requirements: 13.1, 13.2, 13.5, 13.9_
  - _Boundary: backend plugin / packaging_
  - _Depends: 13.2, 13.3, 13.4_
  - _Validation: connector release gates + GOWORK=off builds_

## 14. Final repository-wide verification and closure

- [ ] 14.1 Run residual-reference and architectural cleanup audit
  - Search production code/config/docs/tests for legacy ids, private Codex endpoints, `codex_cli_rs`, `chatgpt-account-id`, old Codex OAuth client id, obsolete `openai_codex.*` extensions, private compaction dialect and default Codex identity prompt.
  - Classify remaining hits as intentional archived/historical evidence or remove them.
  - Observable completion: zero unexplained production references remain.
  - _Requirements: 13.7, 13.8, 14.7_
  - _Boundary: repository-wide verification_
  - _Depends: 13.5_
  - _Validation: scripted grep/architecture check_

- [ ] 14.2 Run release-quality gates
  - Run new connector tests, root unit suite, parity, quality checks, connector release gates, race tests and appropriate wide QA; recheck logs/captures for SIWC secret leakage.
  - Observable completion: deterministic required gates pass and Task 11 live evidence remains tied to the release lineage.
  - _Requirements: 14.1, 14.2, 14.3, 14.4, 14.5, 14.6, 14.8_
  - _Boundary: tests / release governance_
  - _Depends: 14.1_
  - _Validation: make quality-checks && make test-unit && make parity-checks && make qa plus connector release gates_

- [ ] 14.3 Final documentation/release review
  - Ensure current docs state supported deployment scope, direct-vs-app-server semantics, profile security, preview limitations, route migration/removal and reauthorization behavior.
  - Observable completion: no current operator guide instructs users to rely on the private Codex subscription connector.
  - _Requirements: 14.9, 14.10_
  - _Boundary: docs / release governance_
  - _Depends: 14.2_
  - _Validation: docs review + link/config checks_