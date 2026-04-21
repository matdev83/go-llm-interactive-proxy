# API Parity Roadmap

## Summary

Bring the Go proxy from "well-tested shared subset" to **spec-documented parity** for the non-realtime LLM API surfaces it already claims to support:

- OpenAI Responses API
- OpenAI Chat Completions API
- Anthropic Messages API
- Gemini `generateContent` / streaming API
- Bedrock `Converse` / `ConverseStream`
- ACP prompt-turn subset only

The roadmap stays architecture-honest:

- protocol `<->` canonical adapters only
- no pairwise translators
- parity claims must be backed by tests and matrices, not by doc wording alone

Every slice follows strict TDD:

1. update parity matrix against normative spec
2. write failing refclient/refbackend tests
3. write failing adapter/conformance tests
4. implement
5. refactor
6. rerun release gates

## Normative Specs And Codebase Alignment

The source of truth for protocol behavior is the vendor documentation already referenced in `.kiro/specs/go-core-reimplementation-v1/research.md`. The parity work must align the following code areas against those exact specs.

### OpenAI Responses API

- Normative spec:
  - Responses API reference
  - Create response
  - Responses streaming
  - Migration guide for Responses vs Chat Completions
- Code that must align:
  - `internal/plugins/frontends/openairesponses`
  - `internal/plugins/backends/openairesponses`
  - `internal/refclient/openairesponses`
  - `internal/refbackend/openairesponses`
  - `internal/testkit/conformance` rows involving `openai-responses`
- Required parity areas:
  - request item types
  - response item types
  - SSE event sequence and terminal markers
  - tool call lifecycle
  - multi-turn tool continuation items
  - usage
  - multimodal request and assistant output where documented

### OpenAI Chat Completions API

- Normative spec:
  - Chat API reference
  - Create chat completion
- Code that must align:
  - `internal/plugins/frontends/openailegacy`
  - `internal/plugins/backends/openailegacy`
  - `internal/refclient/openaichat`
  - `internal/refbackend/openaichat`
  - `internal/testkit/conformance` rows involving `openai-legacy`
- Required parity areas:
  - assistant tool-call history
  - tool result history
  - `tool_choice`
  - stream chunks and finish reasons
  - usage propagation
  - multimodal message parts

### Anthropic Messages API

- Normative spec:
  - Anthropic Messages API reference
- Code that must align:
  - `internal/plugins/frontends/anthropic`
  - `internal/plugins/backends/anthropic`
  - `internal/refclient/anthropicmessages`
  - `internal/refbackend/anthropicmessages`
  - `internal/testkit/conformance` rows involving `anthropic`
- Required parity areas:
  - message content blocks
  - tool use and tool result history
  - SSE event order and semantics
  - usage and stop reasons
  - multimodal image/document handling
  - documented headers and error shapes

### Gemini `generateContent`

- Normative spec:
  - Gemini API docs hub
  - REST method catalog
  - Text generation guide
- Code that must align:
  - `internal/plugins/frontends/gemini`
  - `internal/plugins/backends/gemini`
  - `internal/refclient/gemini`
  - `internal/refbackend/gemini`
  - `internal/testkit/conformance` rows involving `gemini`
- Required parity areas:
  - `contents`, `systemInstruction`, `generationConfig`
  - function call / function response turns
  - stream chunk framing
  - `usageMetadata`
  - inline multimodal inputs and assistant outputs
  - documented tool config behavior

### Bedrock Converse / ConverseStream

- Normative spec:
  - Bedrock `Converse`
  - Bedrock `ConverseStream`
  - Conversation inference user guide
- Code that must align:
  - `internal/plugins/backends/bedrock`
  - `internal/refbackend/bedrock`
  - `internal/testkit/conformance` rows involving `bedrock`
- Required parity areas:
  - content block mapping
  - eventstream semantics
  - tool use lifecycle
  - usage metadata
  - multimodal request and assistant output behavior supported by Converse

### ACP Prompt-Turn Subset

- Normative spec:
  - ACP overview
  - ACP schema
  - ACP transports
  - ACP prompt-turn behavior used by current connector subset
- Code that must align:
  - `internal/plugins/backends/acp`
  - `internal/refbackend/acp`
  - `internal/testkit/conformance` rows involving `acp`
- Required parity areas:
  - initialize
  - authenticate
  - session creation / reuse
  - prompt turn
  - progress updates
  - cancel
  - resource/reference payloads in the declared subset
- Explicit boundary:
  - no terminal/filesystem/slash-command/full-agent parity in this roadmap

### Shared Canonical And Evidence Layers

- Canonical model that must align with all supported specs:
  - `pkg/lipapi`
  - `internal/core/runtime`
  - capability negotiation and collector logic under `internal/core/*`
- Evidence system that must align with parity claims:
  - `internal/refclient/*`
  - `internal/refbackend/*`
  - `internal/testkit/conformance/*`
  - `.kiro/specs/go-core-reimplementation-v1/refclient-spec-matrix.md`
  - `.kiro/specs/go-core-reimplementation-v1/refbackend-spec-matrix.md`
  - `.kiro/specs/go-core-reimplementation-v1/research.md`
  - `.kiro/specs/go-core-reimplementation-v1/VALIDATION_REVIEW.md`

## Deliverables

### 1. Parity Spec Package

- A dedicated parity spec under `.kiro/specs/` that defines:
  - protocol-by-protocol feature matrices
  - exact in-scope vs out-of-scope boundaries
  - traceability from spec rows to tests and code areas

### 2. Canonical Model Expansion

- `pkg/lipapi` extended so all claimed parity behavior is representable without undocumented loss.
- New canonical support where needed for:
  - assistant multimodal output items and deltas
  - richer tool-use history
  - finish/stop/completion semantics
  - structured usage metadata
  - protocol item kinds currently trapped in vendor-specific exceptions

### 3. Protocol Reference Harnesses

- `internal/refclient/*` upgraded from smoke helpers to protocol reference clients.
- `internal/refbackend/*` upgraded from subset stubs to parity-oriented backend emulators.
- Each protocol harness able to prove:
  - happy path
  - streaming
  - auth
  - error shape
  - tools
  - history continuation
  - usage
  - multimodal request and assistant output where documented

### 4. Adapter And Conformance Coverage

- Frontend and backend adapter package tests expanded to cover every parity row relevant to that adapter.
- `internal/testkit/conformance` split into:
  - shared cross-API translation matrix
  - protocol-specific parity suites for non-universal semantics

### 5. Updated Evidence Docs

- The existing matrices and validation docs updated so every claim is one of:
  - implemented and proxy-proven
  - implemented and direct-wire-proven only
  - planned
  - out of scope

### 6. Release Gates For Parity Claims

- CI gates updated so no protocol can be called parity-ready unless:
  - all relevant parity rows are green
  - race passes
  - critical fuzz passes
  - migration/replay fixtures are updated where behavior changed

## Phased Implementation Plan

### Phase 1. Establish Parity Contracts Before Code Changes

- Add a parity spec and roadmap artifact set under `.kiro/specs/`.
- For each protocol, create a normative parity matrix derived only from official docs and official SDK-observable wire behavior.
- Replace the current "subset vs spec" narrative with explicit status per feature row.

Deliverables:

- parity matrices for all supported protocol families
- traceability from matrix rows to code areas and tests
- explicit out-of-scope list for ACP subset and any excluded vendor features

Acceptance criteria:

- every supported protocol has a machine-readable or reviewable parity matrix
- every row names the normative vendor spec section it is derived from
- every row names the code area responsible for satisfying it
- no existing doc claims parity for behavior that is not represented in the matrix

### Phase 2. Expand The Canonical Model So Parity Is Representable

- Extend `pkg/lipapi` and adjacent core packages so the canonical form can carry the behaviors currently blocked by subset caveats.
- Keep protocol-specific leftovers in explicit vendor extensions only when truly unavoidable.

Deliverables:

- canonical types and invariants for missing parity behavior
- RED/GREEN unit tests for canonical representation and collection rules
- updated capability negotiation where parity requires new expressiveness

Acceptance criteria:

- every in-scope parity row is either canonically representable or explicitly documented as vendor extension only
- no adapter must silently drop supported semantics because `lipapi` cannot represent them
- multi-turn tool history and assistant multimodal output are representable where claimed

### Phase 3. Upgrade The Evidence System From Subset Conformance To Parity Conformance

- Strengthen `internal/refclient/*` and `internal/refbackend/*`.
- Add wire-level protocol parity suites in addition to the shared FE×BE matrix.

Deliverables:

- protocol reference client tests
- protocol reference backend tests
- protocol-specific parity suite under `internal/testkit/conformance` or adjacent parity packages

Acceptance criteria:

- each protocol has direct wire-level evidence for the parity rows claimed as implemented
- multimodal assistant output is proven at direct-wire level wherever the matrix marks it implemented
- shared matrix remains for translation safety, but protocol-specific semantics are no longer hidden behind subset-only rows

### Phase 4. Execute Protocol Tracks In Fixed Order

#### 4A. OpenAI Family

- OpenAI Responses and Chat first because they drive canonical history and item-model changes.

Deliverables:

- parity-complete frontend/backend/refclient/refbackend coverage for OpenAI Responses
- parity-complete frontend/backend/refclient/refbackend coverage for Chat Completions

Acceptance criteria:

- request/response item types match documented surfaces
- streaming event order and terminal semantics match docs
- assistant tool history and tool-result continuation round-trip through canonical form
- usage and finish reasons are preserved without undocumented loss

#### 4B. Anthropic And Gemini

- Align content blocks, tool streaming, usage, stop reasons, and multimodal assistant output behavior.

Deliverables:

- parity-complete Anthropic frontend/backend/reference harnesses
- parity-complete Gemini frontend/backend/reference harnesses

Acceptance criteria:

- current documented losses are removed or explicitly marked out of scope in the matrix
- streaming and non-streaming behavior match vendor docs for the claimed surface
- multimodal assistant outputs are proxy-proven where the matrix marks them implemented

#### 4C. Bedrock

- Reach parity for the documented `Converse` / `ConverseStream` surface used by the backend connector.

Deliverables:

- parity-complete Bedrock backend tests and emulator coverage

Acceptance criteria:

- tool use, eventstream, usage, stop semantics, and supported multimodal behavior match the Bedrock docs
- no Bedrock parity claim depends only on smoke coverage

#### 4D. ACP Subset

- Keep ACP bounded to prompt-turn/session/auth/cancel/progress/reference/resource behavior only.

Deliverables:

- explicit ACP subset parity matrix
- ACP subset reference harness and backend coverage aligned to that matrix

Acceptance criteria:

- every ACP claim is explicitly bounded to the subset
- excluded ACP families are listed in the matrix and never implied by parity wording

### Phase 5. Make TDD And Release Gating Stricter Than V1

- For every parity feature row, implementation starts only after RED tests exist at the right layers.
- CI gates must block parity claims unless all required suites are green.

Deliverables:

- TDD workflow rules for parity tasks
- CI gate updates
- release checklist for marking a protocol parity-ready

Acceptance criteria:

- no parity task is considered complete without failing tests written first
- no protocol is marked parity-ready unless all of its matrix rows are green or explicitly out of scope
- docs, matrices, and CI status always agree

## Required Test Layers

Every parity feature should be covered at the appropriate layers:

- `pkg/lipapi` and core unit tests:
  - canonical representability
  - invariants
  - collection and capability behavior
- `internal/refclient/*`:
  - client-observable protocol correctness
- `internal/refbackend/*`:
  - backend wire-shape correctness
- `internal/plugins/frontends/*`:
  - decode/encode correctness against canonical model
- `internal/plugins/backends/*`:
  - request mapping and event mapping correctness
- `internal/testkit/conformance/*`:
  - cross-API translation safety
  - protocol-specific parity suites for non-universal semantics
- `testdata/` and replay fixtures:
  - migration and regression evidence from Python proxy and vendor-shaped captures

## Acceptance Criteria For The Roadmap As A Whole

This roadmap is complete only when all of the following are true:

- every supported protocol family has a parity matrix tied to official vendor specs
- every parity matrix row points to the owning code area in this repository
- every "implemented" row has matching automated evidence
- `pkg/lipapi` can represent every parity behavior claimed as proxy-supported
- shared FE×BE conformance and protocol-specific parity suites both pass
- release gates prevent parity claims from outrunning evidence
- ACP remains clearly bounded to subset parity only

## Assumptions

- "On par with reference implementations" means **spec-documented API surface parity** for the already-supported non-realtime protocol families, not only the current shared subset.
- ACP remains **subset parity only** in this roadmap.
- This is one master roadmap with shared foundation first, then protocol tracks in this order:
  - OpenAI family
  - Anthropic and Gemini
  - Bedrock
  - ACP subset
- Official vendor docs and official SDK-observable wire behavior are the source of truth.
- Python proxy behavior is regression input, not the normative authority.
- Stage-two architectural work and this parity roadmap should be merged where they overlap, but parity-critical canonical and evidence work takes precedence whenever current architecture blocks truthful protocol claims.
