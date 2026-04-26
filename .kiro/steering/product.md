# Product Overview

**LLM Interactive Proxy (Go)** is a universal control plane for LLM traffic.
It sits between existing AI clients and provider backends so operators can keep client integrations stable
while changing routing, protocol translation, resilience behavior, observability, and traffic control at the proxy layer.

## Product identity

This project is not a generic API gateway and not just a translation shim.
Its differentiator is a small, policy-owning Go core that can:

- accept multiple client-facing LLM protocols,
- translate through one canonical request and event model,
- route and recover before output using core-owned policy,
- expose extensibility through explicit plugin and hook seams,
- keep provider and transport specifics at the edge.

## Core promise

- Keep clients stable: point clients at one endpoint instead of rewriting them.
- Stay backend-flexible: route and translate across provider families and API styles.
- Fail clearly: capability mismatches and required semantic loss must fail explicitly.
- Improve UX under failure: recover from pre-output backend failures without hiding post-output failures.
- Preserve evidence: make routing, attempts, recovery, and surfaced outcomes observable and testable.
- Stay maintainable: keep the core small, boring, and hard to accidentally break.

## Capability pillars

### 1. Multi-frontend compatibility

The standard distribution is expected to expose these client-facing APIs:
- OpenAI Responses API
- legacy OpenAI-compatible chat/models surfaces
- Anthropic Messages API
- Gemini generateContent-style API surface

### 2. Multi-backend orchestration

The runtime is expected to target these backend API flavors:
- OpenAI Responses API
- legacy OpenAI-compatible APIs
- Anthropic Messages API
- Gemini API
- Bedrock API
- ACP (Agent Client Protocol)

### 3. Canonical-in-the-middle translation

The product bridges protocol families by translating through a canonical request model and a canonical event stream.
This canonical middle is the main interoperability contract for the project.

### 4. Streaming-first execution

Streaming is the default execution path for frontend and backend integrations.
Non-streaming behavior is collection over the same canonical stream path, not a second execution path.

### 5. Core-owned routing and continuity

The Go core intentionally preserves these distinctive LIP features:
- dynamic request routing,
- weighted load balancing,
- ordered failover,
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
- route and traffic observers,
- session and continuity stores,
- state, workspace, and auxiliary service facades.

The core owns the legal extension pipeline and ordering. Feature implementations stay outside core policy.

### 7. Secure operator trust boundaries

The Go version now treats session authority and startup posture as core product behavior, not optional polish:
- proxy-owned secure sessions validate resume authority before backend execution,
- diagnostics and privileged inventory require explicit trust boundaries,
- local no-auth operation is constrained to loopback single-user mode,
- startup fails closed for administrative-user and credential-posture risks.

## Primary users and use cases

- Teams with mixed AI clients and mixed backend providers.
- Operator-managed deployments that need routing control, resilience, and observability.
- Agent-heavy workflows that benefit from stable client behavior across backend changes.
- Migration periods where teams need protocol translation without rewriting clients.
- Platform teams that want new behavior to be added through hooks or plugins without destabilizing the proxy core.

## Current product direction

The Go re-implementation is no longer only in bootstrap/porting mode. The current direction is to harden
the runnable standard distribution while keeping the core small.
Its current priorities are:

- preserve the small, stable, policy-owning core while adding hardening features,
- make plugins first-class and explicitly wired through per-composition-root registries,
- preserve the routing, continuity, secure-session, and recovery behavior that makes LIP distinctive,
- keep canonical contracts and plugin contracts stable and minimal,
- use idiomatic Go and official SDKs only at adapter boundaries,
- treat streaming behavior, startup safety, architecture guardrails, and contract tests as primary constraints,
- evolve the stage-four extension platform through typed SDK facades and reference plugins,
- adopt hexagonal architecture pragmatically, without package churn that does not buy maintainability.

## Non-goals for the near-term core version

- port every historical Python feature before the new boundaries are proven,
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

---
_Initial Go steering version: 2026-04-20_
_Updated 2026-04-23: product identity, current direction, pragmatic hexagonal stance, core-vs-edge ownership wording._
_Reason: steering now needs to reflect the current brownfield architecture goals, not only the original greenfield bootstrap intent._
_Updated 2026-04-26: captured secure-session, startup-safety, and stage-four extension-platform maturity._
_Reason: the Go runtime is now a hardened runnable distribution, not just an initial Python-port scaffold._
