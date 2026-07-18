# Requirements Document

## Cursor SDK Backend

Spec directory: `cursor-sdk-backend`

## Introduction

This specification defines an experimental Cursor backend that integrates Go-LIP with Cursor's official agent SDK while preserving the existing `cursorcliacp` backend. The feature is intended to evaluate whether an SDK-backed integration provides a more maintainable and capable Cursor surface without assuming that an SDK removes subprocess lifecycle work or is inherently more reliable than the established ACP path.

The first delivery is a local-agent backend only. It uses a project-owned, versioned bridge process because the official Cursor agent SDK is not a Go library. The bridge and all Cursor SDK types remain inside the backend adapter boundary. Go-LIP continues to expose one canonical request model, one canonical event stream, core-owned routing/failover, and backend-qualified model inventory.

## Boundary Context

- **In scope**: a new `cursorsdk` backend kind; a project-owned Node bridge over the official Cursor SDK; static SDK API-key configuration; structured model discovery; local agent creation and reuse; canonical text/reasoning/usage streaming; workspace and configured MCP support; cancellation; bounded process and agent lifecycle; diagnostics; deterministic and opt-in live validation; coexistence and evidence-based migration gates.
- **Out of scope**: replacing or deleting `cursorcliacp`; automatic fallback between Cursor connectors; Cursor Cloud agents; SDK `Agent.resume` across Go-LIP restarts; SDK custom tools or callbacks into Go-LIP tools; canonical client tool-call passthrough; automatic npm installation; remote bridge hosting; new public canonical request/event concepts; frontend changes.
- **Boundary ownership**: primarily backend plugin and config/wiring, with one narrow internal backend-lifecycle seam for composition-root shutdown ownership.
- **Optional hexagonal lens**: `cursorsdk` is a driven adapter; the Node bridge is an adapter-private anti-corruption layer; `internal/standardplugins` and `internal/infra/runtimebundle` are composition roots; routing and output commitment remain core application orchestration.
- **Revalidation triggers**: backend registration and security posture, model inventory aggregation, streaming/event ordering, cancellation, no-retry-after-output, process shutdown, configuration redaction, race/goroutine hygiene, and cross-platform packaging.

## Architectural Constraints

1. The feature shall not add Cursor SDK or bridge types to `pkg/lipapi`, `pkg/lipsdk`, or provider-neutral core policy.
2. The feature shall not force the SDK protocol through the ACP protocol abstraction merely because both integrations use a subprocess.
3. The feature shall preserve the existing Cursor ACP connector as an independently selectable implementation until explicit replacement gates are satisfied.
4. The canonical transcript presented by Go-LIP shall remain the continuity source of truth for the first delivery.
5. The feature shall not claim reliability superiority without comparative evidence from repeatable tests and dogfood operation.

## Requirements

### Requirement ID Convention

Each acceptance criterion is labeled **`N.M`**. These IDs are the stable handles used by `design.md`, `tasks.md`, and validation evidence.

---

### Requirement 1: The SDK backend shall coexist with the ACP backend

**Objective:** As an operator, I want an explicit alternate Cursor backend, so that I can evaluate the SDK path without losing the established ACP integration.

#### Acceptance Criteria

**1.1.** The standard distribution shall register the SDK implementation under the distinct backend kind `cursorsdk`.

**1.2.** While `cursorcliacp` remains registered, the system shall preserve its existing configuration, authentication, model resolution, and route-selection behavior.

**1.3.** When both Cursor backend kinds are configured, the system shall require routes to identify the intended backend instance and shall not silently choose one connector based only on the model name.

**1.4.** If one Cursor connector fails before output, the system shall apply only the operator-configured core routing plan and shall not perform connector-local fallback to the other Cursor connector.

**1.5.** The SDK backend shall be documented and surfaced as experimental until the replacement criteria in Requirement 12 are met.

**1.6.** The system shall not deprecate or remove `cursorcliacp` as part of this specification.

---

### Requirement 2: The Cursor SDK shall remain behind a versioned bridge boundary

**Objective:** As a maintainer, I want the non-Go SDK isolated behind a narrow contract, so that SDK churn and Node runtime behavior do not leak through Go-LIP architecture.

#### Acceptance Criteria

**2.1.** Where the `cursorsdk` backend is enabled, the system shall invoke the official Cursor SDK only from a project-owned Node bridge located within the backend integration boundary.

**2.2.** The bridge protocol shall use explicit schema and implementation versions and shall reject incompatible peers before creating an agent or sending a prompt.

**2.3.** The Go side of the bridge contract shall contain only adapter-owned transport DTOs and shall not import Node, TypeScript, Cursor SDK, or Cursor wire types into canonical or core packages.

**2.4.** The bridge shall communicate through a bounded, structured streaming protocol rather than terminal-screen parsing or human-readable CLI output parsing.

**2.5.** If the bridge emits malformed, oversized, out-of-order, or unknown mandatory protocol messages, the backend shall fail the affected attempt with a classified adapter error and shall not forward malformed data as canonical output.

**2.6.** The system shall pin the bridge package and official Cursor SDK to validated exact versions and shall expose their versions in safe diagnostics.

**2.7.** The bridge boundary shall not be represented as eliminating subprocess management; the Go runtime shall retain explicit process ownership, health, cancellation, and shutdown responsibilities.

---

### Requirement 3: Installation, authentication, and configuration shall be explicit and secret-safe

**Objective:** As an operator, I want predictable setup and authentication rules, so that enabling the SDK backend does not silently reuse incompatible credentials or mutate my environment.

#### Acceptance Criteria

**3.1.** When the SDK backend is configured without an explicit API key, the standard composition root shall use `CURSOR_API_KEY` as the default static credential source.

**3.2.** The SDK backend shall not treat Cursor CLI login, Cursor Desktop login, or `cursorcliacp` authentication state as a substitute for a Cursor SDK API key.

**3.3.** If no SDK API key is available, the backend factory or first required SDK operation shall return an actionable configuration error without including secret values.

**3.4.** The standard backend registration shall declare static credential posture and local-only access scope for the first delivery.

**3.5.** The system shall support an explicit bridge executable path and a PATH-based default lookup without invoking a shell.

**3.6.** The runtime shall not download, install, upgrade, or execute npm lifecycle installation as a side effect of config validation, startup, model discovery, or request handling.

**3.7.** The system shall validate configured timeouts, agent limits, bridge paths, workspace policy, safety mode, and bridge protocol version before serving a configured SDK backend where validation can be completed locally.

**3.8.** API keys, prompts, tool arguments, workspace contents, and SDK state shall not appear in startup errors, routine logs, metric labels, model discovery warnings, or process command-line arguments.

---

### Requirement 4: Model inventory and capability claims shall be structured and evidence-based

**Objective:** As an operator, I want accurate Cursor model and capability metadata, so that routing does not depend on CLI presentation formats or unsupported assumptions.

#### Acceptance Criteria

**4.1.** When model inventory is loaded, the SDK backend shall obtain model identifiers through a structured SDK or bridge operation and shall not parse `agent --list-models` output.

**4.2.** The SDK backend shall publish canonical model IDs using the existing `cursor/<model>` vendor namespace and shall preserve the SDK-native model ID separately.

**4.3.** The SDK backend shall claim a backend route prefix distinct from `cursorcliacp`, while allowing the model registry to retain separate backend-qualified rows for the same canonical model ID.

**4.4.** If live SDK inventory is unavailable, the backend shall return a fail-soft inventory outcome with stable error codes and shall not fabricate successful discovery.

**4.5.** Where an operator supplies a static model inventory override, the backend shall validate and use it through the existing model-inventory contract.

**4.6.** The backend shall advertise only canonical capabilities that the selected model and implemented bridge mapping can honor losslessly.

**4.7.** If vision, documents, structured output, canonical tools, or parallel tool calls are not proven by implementation and tests, the backend shall omit those capabilities so core negotiation rejects or downgrades requests according to existing policy.

**4.8.** Reasoning effort and model-specific parameters shall be mapped only where the exact SDK model contract exposes a verified equivalent; unsupported values shall not be silently reinterpreted.

---

### Requirement 5: Agent reuse shall preserve canonical transcript authority

**Objective:** As a maintainer, I want controlled SDK agent reuse, so that multi-turn sessions gain continuity without allowing hidden SDK state to override Go-LIP history.

#### Acceptance Criteria

**5.1.** When a new SDK agent is created, the backend shall bootstrap it from a deterministic, bounded representation of the canonical instructions and transcript available to that attempt.

**5.2.** When the same client session, workspace, model, credential identity, safety configuration, settings sources, and MCP surface continue without transcript divergence, the backend may reuse the same in-process SDK agent and send only the new turn content.

**5.3.** If the canonical transcript is edited, truncated, reordered, compacted, or otherwise diverges from the backend's committed history marker, the backend shall invalidate the existing agent and bootstrap a fresh agent from the current canonical transcript.

**5.4.** If any agent-pool identity input changes, the backend shall not reuse the prior agent for the changed configuration.

**5.5.** The backend shall commit its incremental history marker only after the SDK accepts the send operation for that turn.

**5.6.** If agent creation or bootstrap fails, the backend shall dispose any partially created agent and shall not record the failed agent as reusable.

**5.7.** The first delivery shall not use SDK `Agent.resume` to restore agents across Go-LIP process restarts.

**5.8.** After a Go-LIP or bridge restart, the backend shall treat SDK agent handles as invalid and shall rebuild continuity from the canonical transcript.

---

### Requirement 6: SDK output shall map into the canonical stream without duplicating agent tools

**Objective:** As a client integrator, I want protocol-neutral streaming from the SDK backend, so that all existing frontends consume Cursor responses through normal Go-LIP event handling.

#### Acceptance Criteria

**6.1.** When an SDK run starts successfully, the backend shall return a `lipapi.ManagedEventStream` and emit canonical response and message start events before content-class events.

**6.2.** When the SDK emits assistant text deltas, the backend shall emit ordered canonical text deltas without buffering the complete response.

**6.3.** When the SDK emits reasoning deltas that are safe and supported for the selected model, the backend shall emit ordered canonical reasoning deltas.

**6.4.** When the SDK reports bounded usage counters, the backend shall map only counters with verified per-turn meaning and shall preserve omitted-versus-zero semantics where available.

**6.5.** When the SDK run terminates normally, the backend shall emit one canonical terminal response event followed by stream EOF.

**6.6.** If the SDK reports native host-tool or configured MCP activity, the backend shall treat that activity as execution internal to the Cursor agent and shall not emit canonical client tool-call events that would cause a frontend client to execute the tool again.

**6.7.** While the backend does not implement canonical client-provided tool execution, it shall omit `CapabilityTools` and `CapabilityParallelToolCalls`.

**6.8.** If internal SDK activity is surfaced for diagnostics, the system shall use bounded, non-content diagnostics that do not expose tool arguments, tool results, file contents, or unbounded provider names.

**6.9.** The non-streaming frontend paths shall continue to collect from the same canonical stream rather than creating a second SDK execution path.

**6.10.** The bridge and backend shall enforce canonical event envelope limits before releasing events to the core.

---

### Requirement 7: Cancellation and failure recovery shall have explicit ownership

**Objective:** As an operator, I want SDK runs and bridge failures to terminate predictably, so that cancellations do not leak processes, agents, or token generation.

#### Acceptance Criteria

**7.1.** When Go-LIP cancels an active SDK stream, the backend shall first invoke the SDK run's provider-native cancellation operation through the bridge.

**7.2.** If provider-native cancellation completes within the configured bound, the stream shall report provider cancellation mode through the existing managed-stream lifecycle contract.

**7.3.** If the bridge is unresponsive during cancellation, the process owner shall use a bounded transport/process termination fallback and shall report transport cancellation mode.

**7.4.** If terminating an unresponsive shared bridge affects other active SDK runs, those streams shall receive explicit surfaced failures and shall not be silently replayed after client-visible output.

**7.5.** If the bridge exits or becomes invalid before the first client-visible event, the backend may return a classified recoverable pre-output failure so the core routing policy can decide whether to try another configured candidate.

**7.6.** If the bridge or SDK run fails after the first client-visible event, the backend shall surface the failure on the committed stream and shall not restart, resend, or switch connectors for that attempt.

**7.7.** After an unexpected bridge exit, the backend shall invalidate all bridge-local agent and run handles before allowing a later request to start a replacement bridge.

**7.8.** Authentication, invalid configuration, unsupported capability, and incompatible bridge-version failures shall not be misclassified as transient process failures.

---

### Requirement 8: Process, agent, and concurrency resources shall be bounded and shut down by the runtime

**Objective:** As an operator, I want bounded lifecycle management, so that an experimental backend cannot accumulate agents, goroutines, pipes, or child processes.

#### Acceptance Criteria

**8.1.** Each configured SDK backend instance shall own at most one bridge process in the first delivery.

**8.2.** The backend shall bound the number of live SDK agents per bridge and shall evict or dispose only idle agents when the configured limit is reached.

**8.3.** While one turn is active for a given agent identity, the backend shall reject or deterministically serialize a conflicting turn without killing the in-use agent.

**8.4.** Different agent identities may run concurrently only within configured global and per-bridge limits.

**8.5.** The bridge process, its stdio pumps, active run readers, agent pool, and timers shall have explicit owners and cancellation paths.

**8.6.** The internal backend contract shall provide an optional shutdown callback that the runtime composition root registers in `Built.Closers`.

**8.7.** When runtime shutdown begins, the SDK backend shall reject new work, cancel active runs within a bound, dispose recorded agents, request bridge shutdown, close transports, and reap the child process.

**8.8.** If graceful bridge shutdown exceeds its deadline, the runtime-owned closer shall terminate and reap the process without targeting an unrelated reused PID.

**8.9.** Backend construction failure after resource creation shall close every resource created before the failure is returned.

**8.10.** The implementation shall provide race and leak evidence for concurrent acquisition, cancellation, bridge restart, idle eviction, and shutdown.

---

### Requirement 9: Workspace, MCP, settings, and local safety behavior shall be explicit

**Objective:** As an operator, I want controlled local-agent context and tool surfaces, so that the SDK backend does not silently inherit ambient Cursor behavior.

#### Acceptance Criteria

**9.1.** The SDK backend shall require an explicit resolved workspace for every agent identity and shall reject missing or invalid workspaces before agent creation.

**9.2.** The backend shall pass configured MCP servers through the bridge at agent creation and shall include the effective MCP surface in agent-pool identity.

**9.3.** The first delivery shall not expose Go-LIP tools through SDK custom tools, local callback servers, or an implicit MCP bridge.

**9.4.** The backend shall default Cursor settings sources to none and shall load user or project Cursor settings only through explicit trusted configuration.

**9.5.** The backend shall expose an explicit sandbox policy whose default requires the validated SDK sandbox path; disabling sandbox enforcement shall require an affirmative local-only operator setting.

**9.6.** Auto-review or equivalent SDK behavior shall be independently configurable and shall not be represented as equivalent to workspace trust or sandbox enforcement.

**9.7.** If a configured setting source, MCP surface, or safety mode cannot be applied, the backend shall fail closed before sending the prompt rather than continuing with a broader ambient surface.

**9.8.** The system shall not forward arbitrary Go-LIP process environment variables into SDK agents or bridge child processes beyond an explicit allowlist required for runtime operation.

---

### Requirement 10: Routing and canonical failure invariants shall remain core-owned

**Objective:** As an architect, I want the SDK backend to behave like a normal driven adapter, so that Cursor-specific lifecycle behavior does not become a second routing engine.

#### Acceptance Criteria

**10.1.** The core runtime shall remain responsible for candidate selection, ordered failover, parallel races, TTFT budgets, output commitment, and B-leg lineage.

**10.2.** The SDK backend shall not create hidden attempts against `cursorcliacp`, another backend instance, or another model when a send fails.

**10.3.** If the backend performs any provider-local action before returning a stream, that action shall remain within the same B-leg and shall not bypass attempt budgets or lineage.

**10.4.** Once client-visible output begins, no SDK agent recreation, bridge restart, model switch, or credential switch shall be used to hide the failure.

**10.5.** Parallel-race loser cancellation shall call the SDK stream lifecycle and shall not persist the losing run as successful session history.

**10.6.** Backend-private agent IDs, run IDs, process IDs, and credential fingerprints shall not become route selector syntax, canonical model IDs, or B-leg candidate identities.

**10.7.** The feature shall not change canonical request or event contracts unless a separately approved specification establishes a cross-provider requirement.

---

### Requirement 11: Diagnostics and operational behavior shall be actionable without leaking content

**Objective:** As an operator, I want enough safe evidence to diagnose the bridge and SDK, so that experimental failures can be compared with ACP behavior.

#### Acceptance Criteria

**11.1.** Safe diagnostics shall identify the configured backend instance, backend kind, bridge protocol version, bridge package version, Cursor SDK version, runtime state, model discovery state, and bounded agent/run counts.

**11.2.** Diagnostics shall distinguish missing bridge, incompatible bridge, missing Node runtime, authentication failure, model discovery failure, agent busy, bridge crash, cancellation timeout, unsupported capability, and SDK-reported run failure using stable low-cardinality codes.

**11.3.** Logs and metrics shall not include prompts, reasoning text, tool arguments, tool results, file content, API keys, full workspace paths, raw provider payloads, or unbounded agent/run IDs.

**11.4.** Where correlation is needed, the system shall use existing trace/A-leg/B-leg identifiers or bounded opaque hashes rather than raw SDK identifiers.

**11.5.** The backend shall expose counters or structured logs sufficient to compare startup failures, pre-output failures, post-output failures, cancellation outcomes, bridge restarts, and agent reuse without claiming causality from a single run.

**11.6.** Operator documentation shall explain the separate SDK API-key and billing posture, Node/bridge prerequisites, local-only scope, safety defaults, and the difference between SDK and ACP connector selection.

---

### Requirement 12: Test evidence shall gate rollout and any future replacement

**Objective:** As a maintainer, I want measurable release gates, so that the project does not replace a proven connector based only on architectural preference.

#### Acceptance Criteria

**12.1.** Implementation tasks shall follow TDD: bridge protocol, Go process ownership, session history, stream mapping, cancellation, configuration, and registration tests shall be written before production behavior.

**12.2.** Default Go tests shall use fake bridge processes or deterministic in-process fixtures and shall not require Node, npm, a Cursor account, or external network access.

**12.3.** Bridge package tests shall mock the official Cursor SDK and shall verify message ordering, version rejection, secret redaction, cancellation, disposal, and process-exit behavior.

**12.4.** Opt-in live tests shall require an explicit SDK API key and shall run separately from the default suite with bounded timeouts and isolated workspaces/state roots.

**12.5.** Release evidence shall include Linux, macOS, and Windows-native bridge startup, streaming, cancellation, crash recovery, and shutdown checks for supported architectures.

**12.6.** The implementation shall add lifecycle-contract coverage for the new official backend and architecture tests proving Cursor SDK imports remain inside the backend bridge boundary.

**12.7.** The SDK backend shall remain experimental until it demonstrates feature parity for the intended replacement scope, no worse security posture, stable installation, and a sustained lower or acceptably equivalent failure/leak rate under representative dogfood workloads.

**12.8.** If the evidence does not demonstrate a clear replacement case, the project shall retain both connectors as intentionally different integrations rather than forcing migration.

**12.9.** Any future default switch or `cursorcliacp` deprecation shall require a separate reviewed change with migration documentation, rollback behavior, and a compatibility window.
