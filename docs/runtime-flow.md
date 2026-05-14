# Runtime flow

This document expands the request lifecycle in `docs/architecture.md` for agents and maintainers working on runtime, frontend, backend, and feature-plugin code.

## Startup flow

`cmd/lipstd` is the standard distribution entrypoint. Its startup path is intentionally explicit:

1. `config.LoadFile` reads YAML into typed config.
2. `routing.ValidateModelAliasesConfig` rejects invalid alias rules before serving.
3. `tracing.Init` and `logging.NewLogger` create process infrastructure.
4. `pluginreg.NewRegistry` creates an isolated registry for this process.
5. `pluginreg.ResolveUpstreamAPIKeysFromEnv` reads fallback hosted-provider keys.
6. `pluginreg.InstallStandardBundleOn` installs official backend, frontend, feature, and auth-renderer factories.
7. `config.RegistrationsFromConfig` selects configured plugin instances.
8. `Registry.MergeFeatureSurface` builds hook and extension chains from configured feature plugins.
9. `runtimebundle.NewBootstrapApp` and `runtimebundle.Build` assemble the executor, stores, model catalog, metrics, tracing client, health, diagnostics, and HTTP-facing runtime bundle.
10. `stdhttp.RunWithRuntime` mounts frontends and diagnostics and serves until context cancellation.

Custom tests and future alternate distributions should follow the same shape: construct an explicit registry or bundle, build an immutable runtime snapshot, then serve.

## Request flow

### 1. HTTP ingress and frontend decode

A bundled frontend owns wire-level details: route path, request body shape, streaming flags, protocol-specific validation, and protocol-specific error rendering. It decodes to `lipapi.Call` and calls the runtime executor. It must not call backend plugins directly.

### 2. Transport auth and principal context

HTTP auth is a transport concern in `internal/stdhttp`. Stable identity crosses into runtime through `pkg/lipsdk/transport/httpauth` and `pkg/lipsdk/execview`, not through `*http.Request` or middleware-specific types.

### 3. Canonical validation and runtime snapshot

The executor validates `lipapi.Call` before orchestration. When present, `extensions.RequestRuntimeSnapshot` is attached to the context. A snapshot is immutable for the request lifetime; reload/rebind behavior must publish a new snapshot instead of mutating an existing one.

### 4. Session, workspace, and A-leg authority

Secure-session preparation is core-owned. The runtime resolves workspace metadata, opens or resumes the authoritative proxy session, maps denials to stable public errors, and fetches the B2BUA A-leg. Client-provided session hints are hints only; they do not authorize resume or A-leg selection.

### 5. Pre-routing mutation and policy stages

Before route planning, the runtime runs configured feature stages in the legal order:

- `session_open` for first-turn labels and bootstrap metadata;
- `submit_request` for brownfield submit hooks;
- `traffic_observation` for canonical client-to-proxy snapshots where configured;
- `tool_catalog_filter` for outbound tool definition policy;
- `request_wide_shaping` for whole-call transforms;
- `route_hinting` for advisory route preferences.

Mutation stages must leave the canonical call valid. Stage runners validate after mutation where they own canonical changes.

### 6. Route planning and capability negotiation

The executor parses the route selector, applies aliases and default backend resolution, expands weighted/failover candidates, applies candidate health, and honors route hints as advisory preferences. For each candidate, capability negotiation and model-catalog eligibility run before backend open. A candidate can be rejected, downgraded attempt-locally, or opened.

### 7. B-leg open and upstream stream

For an eligible candidate, the executor allocates the next B-leg, runs request-part hooks, merges route query parameters into generation options, emits proxy-to-backend traffic observations when configured, and calls the selected backend's `Open` method.

Backends return `lipapi.EventStream`. They translate provider SDK or wire events into canonical events and classify recoverable pre-output failures with enough metadata for routing and diagnostics.

### 8. Stream receive path

The returned stream wrapper preserves the no-retry-after-output invariant. Before first output, recoverable open or recv failures may consume additional B-legs and try another candidate. After first output, failures are terminal for that stream.

On received events, the stream path applies response-part hooks, tool reactors, completion gates, secure-session recording, traffic observation, and attempt outcome recording where those handlers are configured. Frontend encoders then render canonical events into legal streaming or collected protocol responses.

### 9. Client-side cancellation

Frontend adapters translate protocol-specific cancel operations into `lipapi.ALegCancelRequest`; core cancellation remains A-leg based. OpenAI Responses cancel accepts the proxy response id from `/v1/responses/{response_id}/cancel` as the primary correlation carrier for normal clients; proxy-issued response ids carry the A-leg and authoritative session binding needed for core authorization. `X-LIP-A-Leg-Id` remains a LIP-private fallback for internal/test clients and older responses. Frontends must not require a private LIP header when the public protocol already carries an opaque response id issued by this proxy.

## Failure model

- Bad client input fails at frontend decode or canonical validation.
- Unsupported required capabilities fail before upstream I/O for the selected candidate.
- Recoverable upstream failures can be swallowed only before output starts.
- Post-output failures surface as terminal stream errors.
- Extension failures follow stage-specific failure policy; fail-open stages log and continue, fail-closed stages reject.
- Panics at extension/backend boundaries are isolated and mapped to structured errors or fail-open skips according to the boundary and stage.

## Where to change behavior

- Change frontend wire decoding or encoding in `internal/plugins/frontends/<id>`.
- Change backend provider mapping in `internal/plugins/backends/<id>`.
- Change shared canonical semantics in `pkg/lipapi` only when multiple protocols need the concept.
- Change plugin author contracts in `pkg/lipsdk`.
- Change route planning, B2BUA, secure-session, or no-retry semantics in `internal/core`.
- Change standard wiring in `cmd/lipstd`, `internal/pluginreg`, `internal/infra/runtimebundle`, or `internal/stdhttp`.
