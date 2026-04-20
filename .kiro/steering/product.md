# Product Overview

**LLM Interactive Proxy (Go)** is a universal control plane for LLM traffic.
It sits between existing AI clients and provider backends so operators can keep client integrations stable
while changing routing, protocol translation, resilience behavior, and advanced traffic control at the proxy layer.

## Core promise

- Keep clients unchanged: point clients at one endpoint instead of rewriting them.
- Stay vendor-independent: translate and route across provider families and API styles.
- Improve UX under failure: recover from pre-output backend failures without exposing transient issues to clients.
- Preserve evidence: make routing and recovery decisions observable and testable.
- Stay extensible: keep the core small and push optional behavior behind plugin and hook seams.

## Capability pillars

### 1. Multi-frontend compatibility

The standard distribution exposes these client-facing APIs:
- OpenAI Responses API
- legacy OpenAI-compatible chat/models surfaces
- Anthropic Messages API
- Gemini generateContent-style API surface

### 2. Multi-backend orchestration

The runtime can target these backend API flavors:
- OpenAI Responses API
- legacy OpenAI-compatible APIs
- Anthropic Messages API
- Gemini API
- Bedrock API
- ACP (Agent Client Protocol)

### 3. Cross-API translation

The product bridges protocol families by translating through a canonical request model and a canonical event stream.
Translation is capability-aware: required semantics must remain lossless or fail explicitly.

### 4. Streaming-first execution

Streaming is the default communication mechanism for frontend and backend integrations.
Non-streaming is treated as a specialized collection mode over the same stream path.

### 5. Distinctive LIP resilience behavior

The Go core intentionally preserves these distinctive LIP features:
- dynamic request routing,
- weighted load balancing,
- ordered failover,
- recoverable pre-output failure swallowing,
- B2BUA-like continuity for multi-attempt backend legs,
- request branching where one client request may lead to multiple related backend attempts.

This goes beyond simple proxying. The proxy is allowed to act as a continuity-preserving traffic orchestrator,
as long as it does not corrupt client-visible protocol guarantees.

### 6. Extensibility without core coupling

Advanced behaviors belong behind explicit seams:
- tool call reactor hooks,
- request submit hooks,
- request-part rewriting hooks,
- response-part rewriting hooks,
- observer hooks,
- session and continuity stores.

The core knows where these hooks run, but it does not know feature-specific behavior.

## Primary use cases

- Teams with mixed AI clients and mixed backend providers.
- Operator-managed deployments that need routing control and failure masking.
- Agent-heavy workflows that benefit from stable client behavior across backend changes.
- Migration periods where teams need protocol translation without rewriting clients.

## Current product direction (greenfield)

The Go re-implementation is not a line-by-line port of the Python codebase.
Its priorities are:
- establish a small, stable core,
- make plugins first-class and truly decoupled,
- preserve the routing and continuity behaviors that make LIP distinctive,
- use idiomatic Go and official SDKs where practical,
- treat streaming and contract tests as primary architecture constraints.

## Non-goals for the first core version

- port every historical Python feature before the new boundaries are proven,
- recreate complex coupling between routing, transforms, and provider adapters,
- support provider-specific edge features in the core,
- use dynamic runtime binary plugins as the primary extension mechanism,
- optimize for maximum feature count at the cost of boundary clarity.

---
_Initial Go steering version: 2026-04-20_
_Reason: bootstrap greenfield Go rewrite based on LIP product intent and agreed boundary-first architecture._
