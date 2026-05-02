# Python feature migration map

This map is the source-of-truth index for where Python-era LIP features should land in the Go architecture. It is intentionally about anchors and readiness, not detailed implementation design.

States:

- `ready` - existing Go extension point supports the feature class; implement or harden as a plugin.
- `needs seam` - add or complete an SDK/runtime seam before feature implementation.
- `do not port yet` - semantics are unstable or depend on earlier proof work.
- `core-owned` - belongs in routing, B2BUA, capability, secure-session, or runtime orchestration.

## Migration table

| Python-era feature | State | Go anchor | Notes |
| --- | --- | --- | --- |
| Protocol translation across OpenAI/Anthropic/Gemini-style APIs | core-owned | `pkg/lipapi`, frontend/backend plugins | Use protocol-to-canonical adapters only; no pairwise translators. |
| Route selector parsing, weighting, failover | core-owned | `internal/core/routing`, `internal/core/runtime` | Already core runtime behavior. |
| B2BUA continuity and attempt lineage | core-owned | `internal/core/continuity`, `internal/core/b2bua`, secure-session manager | Pre-output recovery only; post-output failures surface. |
| Capability negotiation and model eligibility | core-owned | `pkg/lipapi`, `internal/core/modelcatalog`, executor negotiation | Unsupported required semantics fail explicitly. |
| Default route and model aliases | core-owned | config + `internal/core/routing` | Keep startup validation. |
| Local API key / transport auth | ready | `internal/stdhttp/auth`, `pkg/lipsdk/transport/httpauth`, `pkg/lipsdk/auth` | Transport-owned; plugins consume principal views. |
| SSO / remote auth | needs seam | auth providers + auth event sink | Keep transport identity separate from canonical mutation. |
| Session-open auto append / first prompt injection | ready | `session.Opener` + `request.Transform`; reference `ref-autoappend-file` | Must append once per authoritative session, not once per B-leg. |
| Static prompt/request suffix rewrites | ready | `request.Transform`, request-part hooks | Prefer whole-call transform for history-aware behavior. |
| Context compaction | ready | `request.Transform` + `state.Store` when persistence is needed | Keep provider-neutral; verify token/capability assumptions. |
| Stale tool-output compaction | ready | `request.Transform` + tool history conventions | Implement after proof plugins harden mutation/revalidation. |
| Dynamic tool-output compression | needs seam | `request.Transform` + `auxiliary.Client` + state | Auxiliary execution is shaped but disabled by default; wire it before real verifier/model calls. |
| Tool catalog filtering | ready | `toolcatalog.Filter`; reference `ref-tool-policy` | Remove/annotate tool definitions before backend translation. |
| Tool call/result rewriting | ready | `hooks.ToolReactor` | Use for stream tool events, not catalog-only policy. |
| Dangerous command handling | needs seam | `toolcatalog.Filter`, `ToolReactor`, possibly dedicated post-model tool-call policy | Do not overload one reactor if command policy needs richer diagnostics/decision contracts. |
| Workspace/project root discovery | ready | `workspace.Resolver`; reference `ref-workspace-guard` | Resolvers discover; policies enforce through transforms/reactors. |
| Sandbox policy | do not port yet | workspace resolver + tool policy + future sandbox runner seam | Needs explicit process/filesystem boundary design. |
| Traffic accounting | ready | `traffic.Observer`, attempt lineage, secure-session views | Keep non-mutating; separate client-proxy and proxy-backend legs. |
| Usage accounting from provider payloads | needs seam | backend usage mapping + observer surface | Avoid provider details in core; normalize where canonical usage exists. |
| Redacted text transcript/capture | ready | `traffic.Observer`, `traffic.Redactor`, `RawCaptureSink`; reference `ref-traffic-transcript` | Redact before persistence. Raw capture is privileged. |
| CBOR/raw wire capture | do not port yet | `RawCaptureSink` + storage policy | Requires explicit retention, redaction, and privilege model. |
| Audit capture | ready | auth events + traffic observers + plugin store | Keep PII and secret redaction rules documented. |
| ProxyMem / long-term memory | needs seam | `state.Store`, `request.Transform`, `completion.Gate`, `auxiliary.Client` | Needs concrete state binding and auxiliary execution policy before porting. |
| Quality verifier calls | needs seam | `completion.Gate` + `auxiliary.Client`; reference `ref-verifier-stub` | Stub exists; real model sub-calls need auxiliary routing policy. |
| Auxiliary routing | needs seam | `auxiliary.Client`, `routehint.Provider`, route roles | Do not let plugins directly call backends. |
| Completion replacement / rejection | ready | `completion.Gate` | Watch buffering limits and stream legality. |
| Request/response redaction | ready | request/response hooks and `traffic.Redactor` | Distinguish mutation from observation redaction. |
| Secret redaction before storage | ready | `traffic.Redactor` + capture sinks | No persistence before redaction. |
| Hook-based no-op/reference plugins | ready | `pkg/lipsdk/hooks`, `feature.FeatureBundle` | Brownfield compatibility remains valid. |
| Per-feature lifecycle | ready | `pkg/lipsdk/plugin.Lifecycle` | Use for startup/shutdown resources, not request mutation. |
| Plugin-scoped state store | needs seam | `pkg/lipsdk/state` | SDK contract exists; default snapshot is disabled unless a concrete store is bound. |
| Managed database-backed plugin state | needs seam | store plugin + runtimebundle binding | Keep separate from continuity/secure-session stores unless explicitly designed. |
| User billing/accounting integration | needs seam | auth principal + traffic/usage observers + external sink | Needs failure policy and PII model. |
| Admin/plugin inventory | ready | diagnostics inventory | Should expose active hooks, observers, stores, auth, capture, and privileges. |
| Live provider smoke tests | ready | `internal/refclient`, config examples, env-gated scripts/tests | Keep optional and skipped without env vars. |

## Implementation rule

No advanced feature should start until this table identifies its anchor. If the state is `needs seam`, write the seam design and tests first. If the state is `do not port yet`, preserve the idea in docs/specs and wait for the prerequisite platform work.

## Next recommended proof set

Before porting high-complexity memory, verifier, sandbox, and dangerous-command behavior, harden these proof plugins:

1. first-session auto append;
2. tool policy;
3. traffic accounting or transcript observation;
4. redacted text capture.

The reference plugin set already covers parts of this space. Prefer promoting reference implementations only after their config, diagnostics, and integration tests meet production expectations.

## Standard distribution proof plugins (alpha dogfood)

These feature plugin IDs are registered by the standard bundle and demonstrate **extension seams** (not production product features). Enable them in YAML under `plugins.features` when proving behavior; see [`internal/plugins/features/REFERENCE_PLUGINS.md`](../internal/plugins/features/REFERENCE_PLUGINS.md) and the **no-key** workflow in [`docs/dogfood-local.md`](dogfood-local.md).

| Plugin ID | Primary seams |
| --- | --- |
| `ref-autoappend-file` | `session.Opener`, `request.Transform` (first logical session request; gated by secure-session `IsNew`) |
| `ref-tool-policy` | `toolcatalog.Filter`, `toolpolicy.Policy`, `hooks.ToolReactor` |
| `ref-traffic-transcript` | `traffic.Observer`, `usage.Observer`, `traffic.Redactor`, `RawCaptureSink` |
| `ref-workspace-guard` | `workspace.Resolver`, transforms, catalog filter, tool reactor |
| `ref-verifier-stub` | `completion.Gate` (stub verifier path) |

Items marked **`do not port yet`** or **`needs seam`** in the migration table above remain **deferred** relative to Python LIP until the listed platform work lands (for example raw CBOR wire capture, ProxyMem, broad auxiliary routing).
