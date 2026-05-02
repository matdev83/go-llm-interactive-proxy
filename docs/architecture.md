# Architecture current state

This document is the operator-facing architecture snapshot for the Go LLM Interactive Proxy. It replaces the early bootstrap note: runtime behavior is implemented and the standard distribution is runnable.

The durable source of truth is split by purpose:

- `AGENTS.md` - agent and repository guardrails.
- `.kiro/steering/*.md` - enduring product, API, routing, structure, tech, and testing memory.
- `.kiro/specs/` - active and archived spec artifacts for feature work.
- `README.md` - current runnable distribution, configuration, security, and QA overview.
- `docs/dogfood-local.md` - canonical **no-key** stub workflow (`lipstd check-config`, `routes`, `inventory`, `serve`) aligned with `config/examples/*.yaml`.
- `docs/architecture.md` - this current-state runtime map.

## Product shape

The Go proxy is a streaming-first control plane between multiple client-facing APIs and multiple backend API families. Frontend adapters decode wire protocols to `pkg/lipapi` canonical calls. Backend adapters translate canonical calls to provider or emulator calls and return canonical event streams. Core orchestration stays provider-agnostic.

The standard distribution (`cmd/lipstd`) wires the official plugin set through `internal/pluginreg`, `internal/infra/runtimebundle`, and `internal/stdhttp`. Core packages do not import concrete plugins or provider SDKs.

## Runtime flow

The implemented request path is:

1. HTTP ingress lands in a bundled frontend mounted by `internal/stdhttp`.
2. Transport/auth middleware attaches principal information through `pkg/lipsdk/transport/httpauth` and `pkg/lipsdk/execview` context contracts.
3. The frontend decodes its wire request into a `pkg/lipapi.Call` and invokes the runtime executor.
4. The executor validates the canonical call and publishes the immutable `internal/core/extensions.RequestRuntimeSnapshot` on the request context.
5. Secure-session preparation resolves principal and workspace context, opens or resumes the authoritative session, and creates or fetches A-leg continuity state.
6. Submit hooks and extension stages run over the canonical call: session open, tool catalog filtering, request-wide shaping, route hinting, and brownfield request-part hooks at their defined positions.
7. Core routing parses the selector, applies default backend/model resolution and model aliases, expands failover candidates, applies route hints as advisory preferences, and enforces the attempt budget.
8. Capability negotiation and model-catalog eligibility checks run per candidate before upstream I/O. Unsupported required semantics reject explicitly or apply attempt-local downgrades when negotiation allows them.
9. The executor allocates a B-leg, emits traffic observations when configured, opens the selected backend, and returns a canonical event stream.
10. Response-part hooks, tool reactors, completion gates, traffic observers, secure-session recording, and attempt lineage run on the stream path where those handlers are configured.
11. Frontend encoders convert canonical events and canonical errors into protocol-legal responses. Non-streaming responses are collected from the same event path.

Recoverable upstream failures may trigger failover only before the first downstream content event is emitted. After output starts, failures are terminal for that attempt and are surfaced through protocol-legal frontend error handling.

## Core-owned behavior

`internal/core` owns orchestration rather than provider semantics:

- routing selector parsing, weighted failover, route hints, candidate health, and max-attempt policy;
- B2BUA A-leg/B-leg continuity, attempt lineage, and pre-output recovery;
- secure-session authority, resume policy, and session-start audit emission;
- capability negotiation, model catalog eligibility, and explicit mismatch failures;
- hook and extension stage execution order, failure policy, timeout boundaries where implemented, and panic isolation;
- canonical event collection, stream error classification, and resource bounds.

These concerns are shared runtime semantics. Provider request shapes, SDK clients, wire payloads, and protocol-specific error rendering stay in adapters and plugins.

## Plugin-owned behavior

Official protocol adapters live under `internal/plugins`:

- frontends: OpenAI Responses, legacy OpenAI-compatible chat/completions, Anthropic Messages, Gemini generateContent;
- backends: OpenAI Responses, legacy OpenAI-compatible, Anthropic, Gemini, Bedrock Converse, ACP prompt-turn;
- features: noop and reference plugins that prove SDK hooks, extension seams, traffic observation, workspace, and completion gates.

The standard distribution may import concrete plugins while assembling the runtime. Core packages must not.

## Extension platform

Feature plugins contribute a `pkg/lipsdk/feature.FeatureBundle`. The current bundle includes:

- brownfield submit, request-part, response-part, and tool-reactor hooks;
- session openers and workspace resolvers;
- tool catalog filters and request-wide transforms;
- route hint providers and completion gates;
- traffic observers, raw capture sinks, redactors, and lifecycles.

The core materializes these into a frozen request runtime snapshot. Hooks mutate or decide, observers record, stores persist, resolvers discover context, and auxiliary clients perform controlled sub-calls. Do not merge those concerns into a single super hook.

See `docs/extension-points.md` and `docs/plugin-authoring.md` for the stage table and authoring rules.

## Composition and startup

`cmd/lipstd` currently performs the standard startup sequence:

1. load YAML config and validate model aliases;
2. initialize tracing and logging;
3. create an isolated `pluginreg.Registry` with `pluginreg.NewRegistry`;
4. resolve default upstream API keys from environment variables;
5. install the standard bundle on that registry;
6. validate mandatory bundled factories;
7. merge configured feature bundles into hooks and extension surfaces;
8. build `runtime.App` and `runtimebundle.Built`;
9. run the HTTP server with `stdhttp.RunWithRuntime`.

The registry is composition-root state, not core global state. Static standard-bundle tables remain under `internal/pluginreg`; future bundle work should keep startup explicit and avoid package-level mutable registries.

## Diagnostics and operations

When enabled by config, diagnostics expose health, attempt lineage, route trace, plugin inventory, model-catalog status, metrics, and pprof paths. Treat diagnostics as operator surfaces: bind them safely, use `diagnostics.shared_secret` outside localhost-only development, and keep labels/cardinality bounded.

Before serving, operators can run **`lipstd check-config`**, **`routes`**, and **`inventory`** against the same YAML (see `docs/dogfood-local.md`) without opening client traffic.

Traffic observation and capture are privileged extension paths. Redaction must happen before persistence or long-term observer storage.

## Architecture boundaries

Permanent rules:

- Core packages do not import concrete plugins.
- Core, `pkg/lipapi`, and `pkg/lipsdk` do not import provider SDKs.
- Protocol adapters translate only protocol-to-canonical or canonical-to-protocol; no pairwise translators.
- Non-streaming behavior is a collector over canonical event streams.
- Capability mismatches fail explicitly.
- Advanced request, response, tool, capture, memory, verifier, and safety features use SDK seams before core logic changes.

Architecture tests under `internal/archtest` and related package tests enforce many of these boundaries. Update tests and steering together when a boundary intentionally changes.
