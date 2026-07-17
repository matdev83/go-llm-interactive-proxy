# Requirements Document

**Source context:** GitHub issue [#157 — Reasoning output preservation](https://github.com/matdev83/go-llm-interactive-proxy/issues/157)

## Introduction

The `reasoning-output-preservation` feature detects when later client requests omit reasoning that the proxy observed in an earlier assistant turn and, when explicitly enabled, can restore the omitted reasoning before forwarding the request to selected backends and models.

The feature is opt-in, exact-match based, bounded, and content-safe. It preserves observed reasoning only; it does not synthesize or infer hidden reasoning.

## Boundary Context

- **In scope**: feature configuration, backend/model matching, built-in compatibility catalog, canonical historical reasoning, response observation, exact matching, request restoration, bounded session state, adapter support, logging, metrics, and tests.
- **Out of scope**: reasoning synthesis, fuzzy matching, cross-session copying, durable distributed state, or exposing hidden reasoning in diagnostics.
- **Boundary ownership**: canonical/SDK contracts plus a feature plugin and adapter-local replay mappings.
- **Revalidation triggers**: canonical contracts, routing/failover, capability negotiation, streaming, adapters, session state, and observability.

## Requirements

### Requirement 1: Opt-In Configuration and Catalog
**Objective:** As an operator, I want to enable preservation only for affected backends/models, so unrelated traffic is unchanged.

#### Acceptance Criteria
1.1. When the feature is absent or disabled, the proxy shall not capture, match, restore, or log reasoning-preservation activity.
1.2. When enabled, the feature shall support `observe` and `restore` actions.
1.3. The feature shall support backend-wide rules and backend/model-keyword rules.
1.4. Model-keyword matching shall be case-insensitive and deterministic.
1.5. The proxy shall ship a versioned built-in catalog seeded with Kimi/Moonshot compatibility entries.
1.6. Explicit operator rules shall override built-in catalog entries.

### Requirement 2: Canonical Reasoning Representation
**Objective:** As an adapter maintainer, I want historical reasoning represented separately from visible content, so supported protocols can replay it safely.

#### Acceptance Criteria
2.1. Canonical assistant messages shall support ordered reasoning parts.
2.2. Reasoning parts shall support text, replay format, signature, and bounded opaque metadata.
2.3. Canonical validation, cloning, sizing, and fuzzing shall include reasoning parts.
2.4. Provider SDK types shall not enter canonical contracts.
2.5. Requests containing historical reasoning shall require reasoning replay support.

### Requirement 3: Stream Capture and State
**Objective:** As an operator, I want winning assistant reasoning retained under the correct session, so it is available for later comparison.

#### Acceptance Criteria
3.1. The proxy shall observe reasoning, visible text, media, and tool-call events without delaying streaming.
3.2. The proxy shall persist only successful winning turns.
3.3. Failed, cancelled, replaced, partial, and parallel-losing turns shall not be persisted.
3.4. State shall be authoritative-session scoped, TTL-expiring, byte-bounded, turn-bounded, and concurrency-safe.
3.5. V1 state shall be process-local and isolated per feature instance.

### Requirement 4: Exact Detection
**Objective:** As a maintainer, I want exact matching between stored turns and client history, so similar but unrelated messages are not rewritten.

#### Acceptance Criteria
4.1. Turn anchors shall exclude reasoning and shall preserve ordered visible/tool content.
4.2. JSON and tool arguments shall be deterministically normalized before hashing.
4.3. A unique exact match without reasoning shall be classified as missing.
4.4. Equivalent reasoning shall be classified as preserved.
4.5. Different reasoning shall be classified as conflicting.
4.6. Multiple possible matches shall be classified as ambiguous.
4.7. Conflicting, ambiguous, or unmatched turns shall not be mutated.
4.8. The feature shall not use fuzzy, semantic, embedding, or LLM matching.

### Requirement 5: Candidate-Specific Restoration
**Objective:** As a routing operator, I want restoration isolated per backend attempt, so retries and parallel routing remain correct.

#### Acceptance Criteria
5.1. Restoration shall run only for matching candidates in restore mode.
5.2. Restoration shall occur before capability negotiation, context eligibility, token preflight, and backend open.
5.3. Restoration shall preserve original reasoning order and shall be idempotent.
5.4. A restored attempt shall not mutate the immutable baseline or another candidate.
5.5. An unrepresentable replay format shall reject or explicitly skip the candidate according to configuration.
5.6. Restored content shall be included in context-size and token-accounting decisions.

### Requirement 6: Adapter Replay Isolation
**Objective:** As a protocol maintainer, I want provider-specific replay metadata kept in the appropriate adapters, so it cannot leak across incompatible backends.

#### Acceptance Criteria
6.1. OpenAI-compatible Chat Completions shall support recognized reasoning text fields.
6.2. OpenAI Responses shall support recognized reasoning input items and opaque replay metadata.
6.3. Anthropic Messages shall support thinking, redacted thinking, and signatures.
6.4. Backends shall declare the replay formats they support.
6.5. Unsupported replay paths shall fail or skip explicitly rather than silently dropping reasoning.
6.6. Streaming and non-streaming behavior shall be parity-tested where legal.

### Requirement 7: Content-Safe Observability
**Objective:** As an operator, I want preservation decisions visible without revealing hidden reasoning, so the feature can be diagnosed safely.

#### Acceptance Criteria
7.1. The proxy shall record observed, preserved, missing, restored, ambiguous, conflicting, unrepresentable, error, and eviction outcomes.
7.2. Logs, metrics, errors, inventory, and diagnostics shall not contain reasoning, signatures, opaque payloads, prompt excerpts, or anchor values.
7.3. Metrics shall use bounded-cardinality labels.
7.4. Diagnostics shall expose only configuration, catalog version, limits, and aggregate counters.

### Requirement 8: TDD and Release Evidence
**Objective:** As a maintainer, I want interfaces and tests first, so implementation remains traceable and regression-resistant.

#### Acceptance Criteria
8.1. Interfaces, canonical contracts, fixtures, and failing tests shall precede production behavior.
8.2. Implementation shall follow red, green, and refactor sequencing.
8.3. Tests shall cover failover, parallel races, streaming, completion gates, state races, adapter goldens, fuzzing, and disabled non-interference.
8.4. Release evidence shall include repository quality, parity, race, and QA gates.
