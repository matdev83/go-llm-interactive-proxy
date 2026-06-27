# Product Overview

**LLM Interactive Proxy (Go)** is a universal control plane for LLM traffic.
It sits between existing AI clients and provider backends so operators can keep client integrations stable
while changing routing, protocol translation, resilience behavior, observability, security posture, and traffic control at the proxy layer.

## Product identity

This project is not a generic API gateway and not just a translation shim.
Its differentiator is a small, policy-owning Go core that can:

- accept multiple client-facing LLM protocols,
- translate through one canonical request and event model,
- route and recover before output using core-owned policy,
- expose extensibility through explicit plugin, SDK, and hook seams,
- keep provider and transport specifics at the edge.

## Core promise

- Keep clients stable: point clients at one endpoint instead of rewriting them.
- Stay backend-flexible: route and translate across hosted providers, local runtimes, compatible APIs, and agent-specific backends.
- Fail clearly: capability mismatches and required semantic loss must fail explicitly.
- Improve UX under failure: recover from pre-output backend failures without hiding post-output failures.
- Preserve evidence: make routing, attempts, recovery, auth decisions, and surfaced outcomes observable and testable.
- Stay maintainable: keep the core small, boring, and hard to accidentally break.

## Capability pillars

### 1. Multi-frontend compatibility

The standard distribution exposes these client-facing API families:

- OpenAI Responses API
- legacy OpenAI-compatible chat/models surfaces
- Anthropic Messages API
- Gemini generateContent-style API surface

### 2. Multi-backend orchestration

The runtime targets several backend families:

- hosted providers: OpenAI Responses, legacy OpenAI-compatible, Anthropic, Gemini, Bedrock, ACP, OpenRouter, NVIDIA, Hugging Face, OpenAI Codex, OpenCode Go/Zen,
- local and compatible runtimes: Ollama (`ollama` / `ollama-cloud`), llama.cpp, LM Studio, vLLM, `localstub`,
- custom OpenAI/Anthropic-compatible backend rows configured by operators.

Exact standard-bundle registration belongs in `internal/pluginreg/standard_table.go`; mandatory distribution ids belong in `pkg/lipsdk/standard_bundle.go`.

### 3. Canonical-in-the-middle translation

The product bridges protocol families by translating through a canonical request model and a canonical event stream.
This canonical middle is the main interoperability contract for the project.

### 4. Streaming-first execution

Streaming is the default execution path for frontend and backend integrations.
Non-streaming behavior is collection over the same canonical stream path, not a second execution path.

### 5. Core-owned routing and continuity

The Go core intentionally owns these distinctive LIP features:

- dynamic request routing,
- weighted load balancing,
- ordered failover,
- parallel backend races,
- TTFT budgets and handicaps,
- model aliases and health/circuit-breaker eligibility,
- recoverable pre-output failure swallowing,
- B2BUA-like continuity for multi-attempt backend legs,
- request branching where one client request may lead to multiple related backend attempts.

This goes beyond simple proxying. The proxy may act as a continuity-preserving traffic orchestrator,
as long as it does not corrupt client-visible protocol guarantees.

### 6. Extensibility without core coupling

Advanced behaviors belong behind explicit seams such as:

- transport authentication and principal attachment,
- session openers and workspace resolvers,
- request submit hooks and request-wide shaping,
- tool catalog filters and tool reactors,
- completion gates and auxiliary requests,
- route hints and traffic observers,
- session, state, and continuity stores,
- model inventory/capability providers,
- token accounting and usage reporting adapters.

The core owns the legal extension pipeline and ordering. Feature implementations stay outside core policy.

### 7. Secure operator trust boundaries

The Go implementation treats session authority and startup posture as core product behavior:

- proxy-owned secure sessions validate resume authority before backend execution,
- diagnostics and privileged inventory require explicit trust boundaries,
- local no-auth operation is constrained to loopback single-user mode,
- startup fails closed for administrative-user, diagnostics, credential-posture, and session-summary risks.

## Primary users and use cases

- Teams with mixed AI clients and mixed backend providers.
- Operator-managed deployments that need routing control, resilience, and observability.
- Agent-heavy workflows that benefit from stable client behavior across backend changes.
- Migration periods where teams need protocol translation without rewriting clients.
- Platform teams that want new behavior to be added through hooks, SDK facades, or plugins without destabilizing the proxy core.

## Current product direction

The Go implementation is a runnable standard distribution, not a bootstrap porting scaffold. Its priorities are:

- preserve the small, stable, policy-owning core while expanding edge adapters,
- make plugins first-class and explicitly wired through per-composition-root registries,
- preserve routing, continuity, secure-session, recovery, and observability behavior that makes LIP distinctive,
- keep canonical contracts and plugin contracts stable and minimal,
- use idiomatic Go and official SDKs only at adapter boundaries,
- treat streaming behavior, startup safety, architecture guardrails, contract tests, and local dogfood as primary constraints,
- evolve the extension platform through typed SDK facades and reference plugins,
- adopt hexagonal architecture pragmatically, without package churn that does not buy maintainability.

## Non-goals for the near-term core version

- claim Python-era features as Go behavior before they are implemented here,
- recreate complex coupling between routing, transforms, and provider adapters,
- move provider-specific or transport-specific semantics into the core,
- force a textbook `app/domain/adapters` package taxonomy across the repo,
- use dynamic runtime binary plugins as the primary extension mechanism,
- optimize for maximum feature count at the cost of boundary clarity and testability.

## Product memory rules

When updating this file:

- preserve the product promise and differentiators,
- keep the core-vs-edge ownership story explicit,
- avoid temporary implementation details and package trivia,
- update when supported compatibility surfaces or core product guarantees change materially.
