# Extension platform authoring guide

This guide is for **operators and feature-plugin authors** wiring behavior on the stage-four extension platform. It complements the normative spec ([`.kiro/specs/go-core-stage-four-feature-extension-platform/design.md`](../.kiro/specs/go-core-stage-four-feature-extension-platform/design.md)), [ADR 0006](adr/0006-stage-four-extension-seam-map-and-migration.md), and [architecture guardrails](architecture-guardrails.md).

## Extension pipeline and legal stages

The core owns a **fixed ordered list** of legal extension stages (requirement **R2**). Inventory exposes this as `extensions.legal_pipeline` and `extensions.stages[]` with `id` and `default_failure` per stage (requirement **R14**).

Canonical order (twelve stages):

1. `transport_auth` — standard HTTP only; identity before decode.
2. `session_open` — session/workspace bootstrap; no direct provider calls (use auxiliary client if model work is needed).
3. `submit_request` — submit hooks; coarse reject/annotate.
4. `tool_catalog_filter` — remove or annotate tools before backend translation.
5. `request_wide_shaping` — request-wide transforms and request-part hooks.
6. `route_hinting` — advisory hints; core routing remains authoritative.
7. `attempt_lifecycle` — core-owned attempt loop (occupancy usually empty for features).
8. `stream_event_mutation` — response-part hooks.
9. `tool_event_reaction` — tool reactors (provider-agnostic contracts).
10. `completion_gating` — bounded buffering and typed completion decisions.
11. `traffic_observation` — four-leg observers, redactors, privileged capture sinks.
12. `egress_encoding` — frontend encode (core-owned).

**Failure policy** per stage is documented in design section **§17** (`FailurePolicyLabel` / `DefaultFailurePolicyForStage` in code). Treat inventory `default_failure` as the operator-visible default; stage runners may narrow further where the contract allows.

## Service facades (when to use which seam)

Use **narrow SDK packages** under `pkg/lipsdk/` — not raw core types, not transport globals, not provider SDKs.

| Concern | Package | Use for |
| --- | --- | --- |
| Session bootstrap | [`pkg/lipsdk/session`](../../pkg/lipsdk/session) | First-turn detection, session labels, opener stage. |
| Workspace metadata | [`pkg/lipsdk/workspace`](../../pkg/lipsdk/workspace) | Project root, markers; consume `WorkspaceView` from resolvers. |
| Request-wide mutation | [`pkg/lipsdk/request`](../../pkg/lipsdk/request) | History-aware shaping distinct from submit hooks. |
| Tool definitions before upstream | [`pkg/lipsdk/toolcatalog`](../../pkg/lipsdk/toolcatalog) | Filter/annotate tools and reconcile tool choice. |
| Tool-use events in stream | [`pkg/lipsdk/hooks`](../../pkg/lipsdk/hooks) | `ToolReactor` — block, rewrite, or pass tool calls/results. |
| Routing intent | [`pkg/lipsdk/routehint`](../../pkg/lipsdk/routehint) | Roles and hints; never bypass core planner rules. |
| Plugin memory | [`pkg/lipsdk/state`](../../pkg/lipsdk/state) | Namespaced TTL state (request/session/principal/global). |
| Private sub-calls | [`pkg/lipsdk/auxiliary`](../../pkg/lipsdk/auxiliary) | Verifier/memory-style calls with lineage; no direct backend handles. |
| Whole-completion control | [`pkg/lipsdk/completion`](../../pkg/lipsdk/completion) | Buffered decisions, replace/replay/reject per typed outcomes. |
| Observation / capture | [`pkg/lipsdk/traffic`](../../pkg/lipsdk/traffic) | Observers vs privileged `CaptureSink`; respect redaction order. |
| Typed bundle assembly | [`pkg/lipsdk/feature`](../../pkg/lipsdk/feature) | `FeatureBundle` + schema version; merge surfaces for registration. |

When a plugin needs state across multiple handlers, bind the shared store with `pkg/lipsdk/state.BindPlugin` using the plugin instance ID before writing or reading keys. That keeps per-plugin namespaces isolated while still letting the plugin share state between its own stages.

**Principal on the request path:** transport attaches identity using [`pkg/lipsdk/transport/httpauth`](../../pkg/lipsdk/transport/httpauth) context helpers. That package is the **stable cross-layer contract** for principal values; HTTP middleware and handler types stay in `internal/stdhttp` (design **§13**).

## Privileged surfaces and inventory

- **General traffic observers** receive redacted or structured views after the redaction stage.
- **Privileged raw capture** (`CaptureSink`) is opt-in and must never drive request mutation (design **§10–§11**).
- **Inventory** (`extensions.features[].privileges`) exposes booleans such as `raw_capture`, `auxiliary_requests`, `completion_gate`, and `auth_provider` so reviewers can see elevated capability (requirement **R14**). `auxiliary_requests` is set for bundles that receive the aux client through request transforms, tool catalog filters, or completion gates.

If your feature needs raw bytes or completion-wide control, declare the matching bundle fields and expect those flags to flip `true` in diagnostics.

## Hook-only plugins and `FeatureBundle` migration

Brownfield rule (design **§15**): existing hook-only plugins remain valid. Registration builds a [`pkg/lipsdk/feature.FeatureBundle`](../../pkg/lipsdk/feature/bundle.go) — often via `FeatureFactoryFromHooks` and YAML decoded into `hooks.Config` (see [`internal/plugins/features/README.md`](../internal/plugins/features/README.md)).

- Empty bundle slices mean **that stage is absent** for that plugin; core must not invent fallback behavior per plugin.
- New seams (session openers, catalog filters, gates, traffic, etc.) are **additional** fields on the same bundle type; migrate incrementally.

## Choosing the right seam (feature → seam map)

If nothing below fits, **extend the platform** (new stage or contract) instead of branching core orchestration (design **§18**).

| Feature class (examples) | Primary seam(s) |
| --- | --- |
| Auto-append first prompt, session labels | `session` opener + `request` transform (+ submit as needed) |
| Outbound rewrite, compaction, secrets on wire | `request` transform, request-part hooks |
| Inbound cleanup, think-tags | Response-part hooks or `completion` gates |
| Allowed/blocked tools | `toolcatalog` filter + `hooks` tool reactor |
| Dangerous commands, steering | Tool reactor + `workspace` view + `state` |
| Project root / sandbox | `workspace` resolver + reactors/filters |
| Quality verifier, replacement completion | `completion` gate + `auxiliary` + `state` |
| SSO / API keys from HTTP | `stdhttp` auth providers → `httpauth` principal → core views |
| Usage / transcripts / CBOR | `traffic` observers and capture sinks; `client_to_proxy` can label both raw ingress and the canonical post-submit snapshot, so capture metadata must distinguish the point of collection |
| Auxiliary routing | `auxiliary` client + route roles + `routehint` |

Reference proof plugins under `internal/plugins/features/` demonstrate each class; see [`REFERENCE_PLUGINS.md`](../internal/plugins/features/REFERENCE_PLUGINS.md).

## Reload-friendly execution snapshots

Each request should run against **one immutable** [`internal/core/extensions.RequestRuntimeSnapshot`](../../internal/core/extensions/snapshot.go) for its lifetime (design **§15B**, quality **Q7**). Composition roots build snapshots from registry + config; mutating wiring after publish should use a **new** snapshot generation, not in-place mutation of shared stage chains.

## Automated guardrails

Architecture tests in [`internal/archtest`](../internal/archtest) and import boundaries in [`internal/core/runtime/boundaries_test.go`](../internal/core/runtime/boundaries_test.go) enforce package rules. See [architecture-guardrails.md](architecture-guardrails.md) for the checklist and how to update budgets when you intentionally grow a layer.
