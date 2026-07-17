# Implementation Plan

- [ ] 1. Lock the exact SDK and bridge contracts with tests first
- [ ] 1.1 Revalidate the pinned Cursor SDK local-agent contract
  - Add an isolated probe harness for the exact pinned SDK version that captures model-list, local agent create/send, delta, step, terminal, usage, cancel, dispose, and shutdown shapes without committing raw account or workspace content.
  - Convert the validated shapes into sanitized fixtures and bridge contract tests before adding production bridge behavior.
  - Record unsupported or ambiguous semantics by narrowing the capability matrix rather than guessing mappings.
  - _Boundary: Backend plugin research and tests_
  - _Depends: none_
  - _Validation: `(cd internal/plugins/backends/cursorsdk/bridge && npm test -- --runInBand)`_
  - _Requirements: 2.6, 4.1, 4.6, 4.7, 4.8, 6.3, 6.4, 7.1, 9.5, 12.1, 12.3, 12.4_

- [ ] 1.2 Define the versioned NDJSON bridge protocol
  - Write failing Go and TypeScript contract tests for initialize/version negotiation, request-response correlation, run event sequence, frame bounds, unknown mandatory messages, duplicate terminals, and safe error envelopes.
  - Define one source-of-truth protocol fixture set consumed by both language test suites without sharing Cursor SDK types.
  - Include models/list, agent/create, agent/send, run/cancel, agent/dispose, bridge/health, and bridge/shutdown methods.
  - _Boundary: Backend plugin bridge contract_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/plugins/backends/cursorsdk/... && (cd internal/plugins/backends/cursorsdk/bridge && npm test)`_
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.7, 6.10, 11.1, 11.2, 12.1, 12.3_

- [ ] 1.3 Build deterministic fake-bridge and SDK-mock test harnesses
  - Add a fake executable that can script startup, models, agent events, malformed frames, blocked cancellation, stderr output, process exit, and shutdown.
  - Add a mocked SDK runtime for Node tests that tracks agent/run creation and proves disposal with no open handles.
  - Ensure default tests require neither Node installation for Go-only packages nor external network/account access.
  - _Boundary: Tests_
  - _Depends: 1.2_
  - _Validation: `go test ./internal/plugins/backends/cursorsdk/... && (cd internal/plugins/backends/cursorsdk/bridge && npm test)`_
  - _Requirements: 2.5, 7.3, 7.4, 7.7, 8.5, 8.9, 8.10, 12.2, 12.3_

- [ ] 2. Add composition-root lifecycle, configuration, and inventory foundations
- [ ] 2.1 Add the optional backend shutdown seam
  - Write failing tests proving a backend closer is collected after construction, invoked once on normal runtime shutdown, and invoked during rollback when later model-runtime assembly fails.
  - Add the additive optional close callback to `execbackend.Backend` and keep nil behavior unchanged for every existing backend.
  - Make backend-construction rollback close already-created resources in reverse order without masking the originating error.
  - _Boundary: Internal core contract and composition root_
  - _Depends: 1.3_
  - _Validation: `go test ./internal/core/execbackend ./internal/infra/runtimebundle ./internal/stdhttp`_
  - _Requirements: 8.5, 8.6, 8.7, 8.8, 8.9_

- [ ] 2.2 Add Cursor SDK key and YAML configuration
  - Write failing tests for `CURSOR_API_KEY` fallback, explicit `api_key` precedence, missing-key redaction, bridge executable lookup, unknown keys, duration/limit bounds, local-only registration, and no ACP config regression.
  - Add the Cursor SDK key to composition-root environment resolution without numbered credential rotation in this phase.
  - Add the `cursorsdk` factory with sandbox-required and empty-setting-source defaults; prohibit shell and runtime npm installation paths.
  - _Boundary: Config and wiring_
  - _Depends: 2.1_
  - _Validation: `go test ./internal/standardplugins ./internal/pluginreg ./internal/infra/runtimebundle`_
  - _Requirements: 1.1, 1.2, 1.5, 2.6, 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 3.8, 9.4, 9.5, 9.6, 9.8, 11.6_

- [ ] 2.3 Add structured SDK model inventory and coexistence tests
  - Write failing tests that SDK and ACP backend instances can publish the same `cursor/...` canonical model while retaining distinct backend kinds, instance provenance, and route prefixes.
  - Implement models/list normalization, accepted-inventory tracking, static override support, and fail-soft operational errors.
  - Add model-aware capability profiles that advertise only the exact mappings proven by Task 1.
  - _Boundary: Backend plugin and model inventory_
  - _Depends: 1.2, 2.2_
  - _Validation: `go test ./internal/plugins/backends/cursorsdk ./internal/core/modelregistry ./internal/infra/runtimebundle`_
  - _Requirements: 1.3, 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 4.7, 4.8, 11.1, 11.2_

- [ ] 3. Implement the project-owned Node SDK bridge
- [ ] 3.1 Implement startup, version reporting, and structured model discovery
  - Start from failing protocol tests for lazy SDK loading, exact package/version reporting, incompatible protocol rejection, missing Node/SDK errors, and model normalization.
  - Keep stdout protocol-only and route bounded diagnostics to stderr.
  - Ensure model discovery uses the official SDK API and never invokes or parses Cursor CLI output.
  - _Boundary: Backend plugin Node bridge_
  - _Depends: 1.1, 1.2_
  - _Validation: `(cd internal/plugins/backends/cursorsdk/bridge && npm test && npm run typecheck)`_
  - _Requirements: 2.1, 2.2, 2.4, 2.5, 2.6, 4.1, 4.4, 11.1, 11.2, 12.3_

- [ ] 3.2 Implement local agent creation and run streaming
  - Add failing tests for workspace/model/API-key/MCP/settings/sandbox option mapping and for create/send state transitions.
  - Normalize verified SDK text, reasoning, usage, activity, warning, terminal, and error surfaces into bridge events with monotonic sequence numbers.
  - Keep SDK-native tool and MCP activity classified as internal activity rather than client tool calls.
  - _Boundary: Backend plugin Node bridge_
  - _Depends: 3.1_
  - _Validation: `(cd internal/plugins/backends/cursorsdk/bridge && npm test && npm run typecheck)`_
  - _Requirements: 3.2, 3.8, 5.1, 5.6, 6.2, 6.3, 6.4, 6.5, 6.6, 6.8, 9.1, 9.2, 9.4, 9.5, 9.6, 9.7_

- [ ] 3.3 Implement run cancel, agent disposal, and bridge shutdown
  - Add failing tests for idempotent cancel/dispose/shutdown, cancellation timeout behavior, rejected new work during shutdown, and unexpected SDK exceptions.
  - Track only bridge-created agents and runs; never sweep an external/global SDK store.
  - Exit cleanly after disposing recorded resources and ensure tests detect open handles or late unhandled failures.
  - _Boundary: Backend plugin Node bridge_
  - _Depends: 3.2_
  - _Validation: `(cd internal/plugins/backends/cursorsdk/bridge && npm test && npm run typecheck)`_
  - _Requirements: 5.6, 5.7, 7.1, 7.2, 7.7, 8.1, 8.5, 8.7, 9.3, 12.3_

- [ ] 3.4 Package the companion bridge reproducibly
  - Add exact dependency pinning, lockfile verification, supported Node engine declaration, executable entry point, and package-content tests.
  - Add a safe `--version`/doctor path that does not load credentials or create agents.
  - Prohibit install-time product behavior beyond ordinary package build requirements and document that Go-LIP never performs installation.
  - _Boundary: Backend plugin packaging_
  - _Depends: 3.1, 3.3_
  - _Validation: `(cd internal/plugins/backends/cursorsdk/bridge && npm ci && npm test && npm pack --dry-run)`_
  - _Requirements: 2.6, 3.5, 3.6, 3.7, 11.1, 11.6, 12.5_

- [ ] 4. Implement Go bridge process and agent lifecycle
- [ ] 4.1 Implement the bridge process owner
  - Add failing tests for startup singleflight, version handshake, bounded stdout/stderr readers, unexpected exit, generation invalidation, restart on a later operation, graceful shutdown, hard-kill fallback, and exactly-once wait.
  - Spawn directly without a shell, construct the allowed environment explicitly, and keep the API key off argv and environment.
  - Make `Close` idempotent and suitable for runtimebundle rollback and shutdown.
  - _Boundary: Backend plugin process runtime_
  - _Depends: 1.3, 2.1, 3.4_
  - _Validation: `go test -race ./internal/plugins/backends/cursorsdk/...`_
  - _Requirements: 2.7, 3.5, 3.8, 7.3, 7.4, 7.7, 8.1, 8.5, 8.7, 8.8, 8.9, 8.10, 9.8_

- [ ] 4.2 Implement canonical history coordination and bounded agent pooling
  - Add failing tests for new bootstrap, incremental sends, transcript edits/truncation/reordering, model/workspace/key/settings/MCP/safety changes, same-key busy conflicts, different-key concurrency, idle eviction, max-agent exhaustion, and bridge-generation changes.
  - Commit history only after send acceptance and invalidate uncertain agents after cancel, run error, or bridge failure.
  - Keep state process-local and explicitly avoid `Agent.resume`.
  - _Boundary: Backend plugin session runtime_
  - _Depends: 4.1_
  - _Validation: `go test -race ./internal/plugins/backends/cursorsdk -run 'History|Session|Pool|Agent'`_
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 5.8, 8.2, 8.3, 8.4, 8.10, 9.2_

- [ ] 4.3 Implement the managed SDK run stream
  - Add failing tests for start/content/usage/terminal order, EOF, frame and event bounds, conservative usage, duplicate/out-of-order events, close, provider cancel, transport cancel, and history commit.
  - Map only verified per-turn usage fields and omit cumulative/full-agent counters.
  - Ensure internal activity cannot become canonical tool calls or leak content into warnings.
  - _Boundary: Backend plugin stream adapter_
  - _Depends: 3.2, 4.1, 4.2_
  - _Validation: `go test -race ./internal/plugins/backends/cursorsdk -run 'Stream|Event|Usage|Cancel'`_
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 6.8, 6.9, 6.10, 7.1, 7.2, 7.3, 7.4, 10.4, 10.5_

- [ ] 4.4 Implement classified bridge and SDK failure recovery
  - Add failing tests for missing executable, incompatible bridge, auth failure, unknown model, busy/limit, process exit before output, process exit after output, protocol violation, cancellation timeout, and restart on the next request.
  - Wrap only safe pre-output transient failures as recoverable; keep auth/config/capability failures non-recoverable.
  - Invalidate all generation-local handles after bridge death and never replay a committed attempt.
  - _Boundary: Backend plugin error adapter_
  - _Depends: 4.1, 4.3_
  - _Validation: `go test ./internal/plugins/backends/cursorsdk -run 'Error|Failure|Crash|Restart'`_
  - _Requirements: 2.5, 3.3, 7.4, 7.5, 7.6, 7.7, 7.8, 10.2, 10.3, 10.4, 11.2, 11.3, 11.4_

- [ ] 5. Assemble the `cursorsdk` backend and enforce its semantic boundaries
- [ ] 5.1 Implement prompt encoding and request validation
  - Add failing tests for deterministic role/order encoding, delimiter escaping, size bounds, unsupported media/files/tool history, and absence of route/credential metadata in model-visible text.
  - Reject non-lossless canonical parts before bridge send.
  - Preserve full bootstrap and incremental-turn behavior from the history coordinator.
  - _Boundary: Backend plugin request adapter_
  - _Depends: 2.3, 4.2_
  - _Validation: `go test ./internal/plugins/backends/cursorsdk -run 'Prompt|Encode|Unsupported|Bound'`_
  - _Requirements: 4.7, 5.1, 5.2, 5.3, 6.7, 9.1, 10.7_

- [ ] 5.2 Assemble backend open, capabilities, inventory, and close
  - Add failing tests that `Open` resolves the accepted native model, validates workspace and capabilities, acquires an agent, starts one run, and returns one managed stream.
  - Wire `ResolveCaps`, model inventory, `BackendPrefixes: cursorsdk`, and the optional close callback.
  - Leave max-output enforcement and unsupported capabilities fail-closed.
  - _Boundary: Backend plugin_
  - _Depends: 2.3, 4.1, 4.3, 4.4, 5.1_
  - _Validation: `go test ./internal/plugins/backends/cursorsdk ./internal/core/modelregistry`_
  - _Requirements: 1.1, 4.2, 4.3, 4.5, 4.6, 4.7, 4.8, 6.1, 6.7, 8.6, 9.1_

- [ ] 5.3 Enforce MCP, settings-source, sandbox, and environment policy
  - Add failing tests for empty default settings sources, explicit trusted sources, normalized MCP identity, sandbox-required default, explicit unsandboxed override, independent auto-review, and environment allowlist behavior.
  - Fail before send when a requested safety or settings option cannot be applied.
  - Confirm the backend exposes no custom-tool callback or implicit Go-LIP MCP bridge.
  - _Boundary: Backend plugin security and config_
  - _Depends: 3.2, 4.1, 5.2_
  - _Validation: `go test ./internal/plugins/backends/cursorsdk ./internal/standardplugins -run 'MCP|Setting|Sandbox|Environment|Security'`_
  - _Requirements: 3.4, 3.7, 3.8, 9.2, 9.3, 9.4, 9.5, 9.6, 9.7, 9.8_

- [ ] 5.4 Add safe operational diagnostics
  - Add tests that logs/status include only stable backend, version, state, count, and outcome fields and reject secrets, prompts, paths, SDK IDs, tool content, and raw payloads.
  - Emit bounded evidence for discovery, agent create/reuse/invalidate/evict, run outcomes, bridge restarts, cancellation mode, and shutdown.
  - Reuse existing trace/A-leg/B-leg correlation rather than exposing SDK IDs.
  - _Boundary: Backend plugin observability_
  - _Depends: 4.4, 5.2_
  - _Validation: `go test ./internal/plugins/backends/cursorsdk ./internal/standardplugins -run 'Diag|Log|Redact|Secret|Status'`_
  - _Requirements: 3.8, 6.8, 11.1, 11.2, 11.3, 11.4, 11.5_

- [ ] 6. Prove routing, lifecycle, and protocol integration
- [ ] 6.1 Add composed Go-LIP integration tests
  - Exercise a fake bridge through standard registration, model registry, executor, and at least one real frontend encode/decode path.
  - Prove SDK and ACP rows coexist, route selection is explicit, no connector-local fallback occurs, and pre-output failures remain core policy.
  - Prove post-output bridge failure surfaces on the committed B-leg and parallel-race losers cancel without history commit.
  - _Boundary: Tests_
  - _Depends: 2.2, 5.2, 5.3_
  - _Validation: `go test ./internal/standardplugins ./internal/infra/runtimebundle ./internal/core/runtime ./internal/stdhttp`_
  - _Requirements: 1.2, 1.3, 1.4, 1.6, 7.5, 7.6, 10.1, 10.2, 10.3, 10.4, 10.5, 10.6, 10.7, 12.2_

- [ ] 6.2 Add lifecycle, architecture, race, leak, and fuzz gates
  - Add `cursorsdk/lifecycle_contract_test.go` and update architecture tests so the official backend is covered without treating the Node bridge as core.
  - Prove Cursor SDK package references occur only below the bridge boundary.
  - Add race/leak tests for startup, pool concurrency, cancel, restart, idle eviction, partial-build rollback, and shutdown; fuzz protocol frames and event mapping.
  - _Boundary: Tests and architecture guards_
  - _Depends: 4.1, 4.2, 4.3, 5.2_
  - _Validation: `go test -race ./internal/plugins/backends/cursorsdk/... && go test ./internal/archtest ./internal/infra/runtimebundle && make test-fuzz`_
  - _Requirements: 2.3, 8.3, 8.4, 8.5, 8.8, 8.9, 8.10, 12.1, 12.2, 12.6_

- [ ] 6.3 Add opt-in live and cross-platform bridge smoke
  - Add isolated live scenarios for discovery, text, verified reasoning, workspace operation under safety policy, configured MCP, cancellation, reuse, hard bridge restart, canonical rebootstrap, and shutdown.
  - Require explicit `CURSOR_API_KEY`, per-scenario workspace/state, strict timeout, and secret-safe artifact handling.
  - Add Linux, macOS, and Windows-native package/start/stream/cancel/crash/shutdown lanes; report missing setup as blocked.
  - _Boundary: Tests and release tooling_
  - _Depends: 3.4, 5.2, 5.3, 6.2_
  - _Validation: `CURSOR_SDK_LIVE=1 make test-cursor-sdk-live && make test-cursor-sdk-platform`_
  - _Requirements: 4.6, 4.8, 7.1, 7.3, 8.7, 9.5, 12.4, 12.5, 12.7_

- [ ] 6.4 Document installation, selection, safety, and known limits
  - Add operator documentation and config examples for bridge installation, exact version checks, API-key/billing separation, local-only registration, explicit route selection, settings/sandbox defaults, MCP, capability omissions, and troubleshooting.
  - State that Go-LIP never installs npm dependencies and that ACP remains available.
  - Document live-test handling and the process-local continuity limitation.
  - _Boundary: Docs_
  - _Depends: 2.2, 3.4, 5.2, 5.3_
  - _Validation: `make quality-checks`_
  - _Requirements: 1.3, 1.5, 1.6, 3.2, 3.5, 3.6, 5.7, 5.8, 9.3, 11.6, 12.8, 12.9_

- [ ] 7. Complete experimental rollout evidence without changing defaults
- [ ] 7.1 Run focused and repository-wide quality gates
  - Run bridge package verification, targeted Go packages, architecture tests, full unit/integration tags, race, fuzz smoke, lint, and vulnerability checks.
  - Fix regressions only within the approved lifecycle/backend/config/test/doc boundaries.
  - Confirm the final diff contains no default-route switch, ACP removal, Cloud support, resume, custom tools, or public canonical changes.
  - _Boundary: Validation_
  - _Depends: 6.1, 6.2, 6.3, 6.4_
  - _Validation: `(cd internal/plugins/backends/cursorsdk/bridge && npm ci && npm run typecheck && npm test && npm pack --dry-run) && make test && make test-race && make qa`_
  - _Requirements: 1.2, 1.5, 1.6, 12.1, 12.2, 12.3, 12.5, 12.6_

- [ ] 7.2 Establish comparative dogfood evidence and retain explicit opt-in
  - Define a repeatable ACP-versus-SDK matrix covering setup, inventory, TTFT, completion latency, pre/post-output failures, cancellation, restart, leaks, continuity, platform defects, and upstream-update maintenance.
  - Record only bounded aggregate results and safe incident classifications; do not collect prompts, tool content, raw workspace paths, keys, or SDK IDs.
  - Keep `cursorsdk` experimental and non-default unless a separate migration proposal demonstrates the replacement gates.
  - _Boundary: Validation and docs_
  - _Depends: 7.1_
  - _Validation: `make test-cursor-sdk-comparison-report`_
  - _Requirements: 1.5, 11.3, 11.4, 11.5, 12.7, 12.8, 12.9_
