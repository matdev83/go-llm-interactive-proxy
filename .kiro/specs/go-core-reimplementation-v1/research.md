# Research Notes: go-core-reimplementation-v1

## Purpose

Capture the reasoning inputs used to generate this spec, summarize what was learned from the current Python LIP repository, and record the external references that shaped the Go-first design.

This file is intentionally background-oriented. The implementation contract lives in `requirements.md`, `design.md`, and `tasks.md`.

## Research inputs

### 1. Kiro / cc-sdd workflow shape

Key observations from the current Kiro-inspired workflow and cc-sdd references:

- The spec workflow is explicitly phase-based: init -> requirements -> design -> tasks -> implementation.
- Specs are treated as **contracts between bounded parts of the system**, not as giant “write all code from this document” prompts.
- The newer cc-sdd design template explicitly emphasizes:
  - boundary-first design,
  - a **File Structure Plan**,
  - tasks that can carry `_Boundary:` and `_Depends:` annotations.
- The LIP repository’s Kiro guidance also emphasizes:
  - spec-first for complex architectural work,
  - TDD,
  - steering as project memory,
  - traceability from tasks back to requirements.

### 2. Current Python LIP product direction

The current repository still clearly values:

- multiple client-facing protocol surfaces,
- multiple backend families,
- cross-protocol flexibility,
- dynamic routing and failover,
- B2BUA-style session handling,
- tool-call-reactor seams,
- capability-driven plugin boundaries,
- and strong debugging / capture / observability posture.

That means the rewrite should preserve the product’s intent but replace the architectural shape.

### 3. Specific current-LIP behaviors worth preserving

#### Routing and failover

The current docs and README show a routing selector model with:

- `backend:model`
- ordered failover using `|`
- weighted routing using `^`
- and a session-aware `[first]` override for weighted selectors

The README also documents an important runtime property:

- if a weighted branch fails **before meaningful output starts**, the request can re-roll within the same logical request using remaining weighted leaves.

This is a distinctive execution behavior and should stay core-owned.

#### Failure handling semantics

The current user-facing failure-handling documentation is explicit about:

- waiting and retrying short pre-output errors,
- immediate failover when wait is too long or another backend is available,
- surfacing errors when no safe recovery is possible,
- **never** silently recovering after content has started,
- emitting streaming keepalive comments during silent wait periods.

These semantics are directly relevant to the Go execution engine.

#### B2BUA session lineage

The current Python codebase already models B2BUA as:

- a core-owned A-leg continuity identity,
- attempt-scoped B-leg session identifiers,
- an attempt record store,
- and session resolution logic.

The Go rewrite should keep the explicit lineage model while simplifying the implementation.

#### Tool call reactor seams

The current tool-call-reactor subsystem documents several good lessons:

- avoid global mutable stream state,
- inject collaborators instead of reading runtime globals,
- keep the reactor path typed and fail-open by default,
- and separate orchestration from specific policy handlers.

The Go design keeps those lessons, but reserves only the hook surfaces in v1.

### 4. Why the Go rewrite should not copy the Python architecture

The Python repository has already moved toward typed boundaries and capability declarations, but it still carries a lot of coupling pressure from historical growth. The rewrite should not port that structure. Instead it should:

- reduce the core to canonical model + execution engine + routing/B2BUA,
- move all protocol behavior into plugins,
- keep future advanced behaviors behind hook APIs,
- and use the current Python repo mainly as a **behavior oracle** and **fixture source**.

## Design conclusions

### Conclusion A: Three classes of ownership are required

To reconcile “small core” with “routing/B2BUA must stay distinctive,” the design uses three ownership classes:

1. **Core-owned semantics**
   - canonical call / event model
   - capability negotiation
   - routing and failover
   - B2BUA lineage
   - diagnostics

2. **Protocol plugins**
   - frontends
   - backends

3. **Feature-hook plugins**
   - submit hooks
   - request/response part altering
   - tool-call reactors

This prevents the most common failure mode: putting too much execution logic into feature plugins or too much feature logic into the core.

### Conclusion B: Streaming must be the single execution path

The rewrite should not have separate streaming and non-streaming semantics. Instead:

- backends emit canonical event streams,
- frontends either stream them directly or collect them,
- all retry/failover rules are expressed relative to “has client-visible content started yet?”

That keeps the model small and matches the current LIP failure-handling posture.

### Conclusion C: No pairwise translators

The current product requirement is “translate between all supported APIs.” The only scalable way to do that is:

- protocol -> canonical
- canonical -> protocol

Pairwise translation would explode in maintenance burden.

### Conclusion D: Do not use Go’s native `plugin` package

For v1, explicit in-process registration is the correct portability/simplicity choice. It avoids portability limits and race-detector drawbacks while preserving small-core boundaries.

### Conclusion E: ACP support should be subset-first

ACP is important, but it has richer concepts than a plain LLM request/response API. The v1 backend should therefore support the prompt-turn subset cleanly and reject unsupported ACP-only features explicitly rather than pretending they map perfectly.

## External reference notes

### Official / primary references used

- cc-sdd repository and spec-driven guide
- current LIP repo `.kiro`, README, AGENTS, feature docs, and recent dev commit history
- official or primary Go SDK references for:
  - OpenAI
  - Anthropic
  - Google Gen AI
  - AWS Bedrock
- official ACP protocol overview and transport guidance
- Go standard `plugin` package documentation

## Source references

### Kiro / cc-sdd

- https://github.com/gotalab/cc-sdd
- https://github.com/gotalab/cc-sdd/blob/main/README.md
- https://github.com/gotalab/cc-sdd/blob/main/docs/guides/spec-driven.md
- https://raw.githubusercontent.com/gotalab/cc-sdd/main/.kiro/settings/templates/specs/design.md
- https://raw.githubusercontent.com/gotalab/cc-sdd/main/.kiro/settings/templates/specs/tasks.md

### Current LIP repo

- https://github.com/matdev83/llm-interactive-proxy
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/README.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/AGENTS.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/.kiro/AGENTS.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/.kiro/steering/product.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/.kiro/steering/structure.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/.kiro/steering/tech.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/.kiro/steering/testing.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/docs/user_guide/features/failure-handling.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/docs/development_guide/routing-selectors.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/src/core/interfaces/b2bua_mapping_store_interface.py
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/src/core/services/b2bua_session_resolver_service.py
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/src/core/services/tool_call_reactor/README.md
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/src/core/interfaces/tool_call_reactor_interface.py
- https://raw.githubusercontent.com/matdev83/llm-interactive-proxy/dev/src/core/interfaces/tool_call_reactor_orchestrator_interface.py
- https://github.com/matdev83/llm-interactive-proxy/commits/dev

### Official / primary Go and protocol references

- https://github.com/openai/openai-go
- https://platform.openai.com/docs/api-reference/responses-streaming
- https://github.com/anthropics/anthropic-sdk-go
- https://github.com/googleapis/go-genai
- https://pkg.go.dev/google.golang.org/genai
- https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ConverseStream.html
- https://agentclientprotocol.com/protocol/overview
- https://agentclientprotocol.com/protocol/transports
- https://agentclientprotocol.com/libraries/community
- https://pkg.go.dev/plugin

## Open questions intentionally left for implementation discovery

1. Exact canonical field naming and package split inside `lipapi`
2. Whether the in-memory B2BUA store uses TTL sweeps or lazy expiration in v1
3. Whether to expose diagnostics via JSON only or add text/debug views
4. Whether the first Gemini frontend subset should include more than the text/tool shared subset
5. Whether ACP session reuse will need a dedicated adapter cache layer in v1
6. Whether to add a tiny helper dependency for SSE framing or keep it entirely stdlib-based

These do not change the architecture contract and can be resolved during implementation as long as the spec boundaries remain intact.
