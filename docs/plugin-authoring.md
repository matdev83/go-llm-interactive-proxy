# Plugin authoring guide

This guide explains how to write feature and protocol plugins that preserve the Go proxy architecture. For the complete stage map, see `docs/extension-points.md`. For operator configuration and examples, see `README.md` and `config/config.yaml`. For the **no-key local stub** maintainer workflow (`check-config`, routes, inventory, serve), see [`docs/dogfood-local.md`](dogfood-local.md).

## Plugin types

- Frontend plugins decode a client protocol into `lipapi.Call` and encode canonical events/errors back to that protocol.
- Backend plugins translate `lipapi.Call` into upstream calls and emit `lipapi.EventStream` values.
- Feature plugins contribute hooks, observers, resolvers, gates, and lifecycles through `pkg/lipsdk/feature.FeatureBundle`.
- Store plugins provide persistence or continuity backends through composition-root wiring.

Only standard distribution packages (`cmd/lipstd`, `internal/pluginreg`, `internal/infra/runtimebundle`, `internal/stdhttp`) should import concrete bundled plugins. Core packages must remain plugin-agnostic.

## Feature bundle basics

A feature factory decodes opaque YAML and returns `feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, ...}`. Empty slices mean the plugin does not occupy that stage.

Use SDK packages, not core internals:

- `pkg/lipsdk/hooks` for brownfield submit, part, and tool-reactor hooks;
- `pkg/lipsdk/session` for session openers;
- `pkg/lipsdk/workspace` for workspace resolvers;
- `pkg/lipsdk/request` for whole-call canonical transforms;
- `pkg/lipsdk/toolcatalog` for tool definition filtering;
- `pkg/lipsdk/toolpolicy` for allow/deny on canonical tool-call lifecycle events before tool reactors;
- `pkg/lipsdk/routehint` for advisory route preferences;
- `pkg/lipsdk/completion` for whole-completion gates;
- `pkg/lipsdk/traffic` for observation, capture, and redaction;
- `pkg/lipsdk/usage` for accounting-style observation over canonical usage deltas (non-mutating);
- `pkg/lipsdk/state` for plugin-scoped state;
- `pkg/lipsdk/auxiliary` for controlled sub-calls.

## Authoring rules

- Keep handlers small, deterministic, and context-aware.
- Never retain or mutate canonical pointers after the handler returns unless the interface explicitly grants ownership.
- Use `context.Context` for cancellation and deadlines; do not start per-request background goroutines from handlers.
- Return classified errors instead of panicking. Core isolates panics, but a panic is still a plugin bug.
- Keep provider SDK types inside backend plugins only.
- Keep HTTP transport types inside frontends or stdhttp transport integrations only.
- Bind state with `state.BindPlugin` before sharing state between handlers in the same feature.
- Redact before persistence. Raw capture is privileged and must be disabled unless explicitly configured.
- Use route hints only as hints; the core planner remains authoritative.
- Revalidate assumptions with focused tests whenever a handler mutates the canonical call or event stream.

## Configuration pattern

Feature plugin configuration should be explicit and typed after YAML decode:

```yaml
plugins:
  features:
    - id: ref-tool-policy
      enabled: true
      config:
        block_names: ["dangerous_shell"]
        block_prefixes: ["admin."]
```

A plugin should reject invalid config at startup rather than fail during the first request.

## Backend model inventory

Backend plugins must expose `execbackend.Backend.ModelInventory` with a `pkg/lipsdk/modelinventory.Provider`
and at least one `execbackend.Backend.BackendPrefixes` entry. Prefixes must match the backend factory id
(for example `openai-responses`, `ollama`, `ollama-cloud`) and must be unique across backend connector
kinds at runtime. Multiple instances of the same connector kind may reuse that kind's prefix.
Canonical model IDs must use the `vendor/model` form; do not publish
inventory rows whose canonical id uses a backend prefix qualifier such as `ollama:google/gemma4`.
The core model registry uses this provider at startup and during background refresh to answer fast routing
lookups for canonical model IDs such as `openai/gpt-5`.

Backend authors should choose one inventory source:

- Remote provider API, such as a `/models` endpoint or provider SDK list operation.
- Backend-specific static config file. File inventories should use an `items:` list; `models:` is accepted as
  a compatibility alias when `items:` is absent.
- Inline static config for fixed local/test backends.

Static providers should use `modelinventory.StaticProvider`, which also marks the inventory as non-refreshable.
Dynamic providers must respect `context.Context`; the runtime applies `model_inventory.fetch_timeout` per backend
inventory fetch.

## Testing expectations

Every feature plugin should have:

- unit tests for config decoding and invalid config;
- stage-runner tests for success, fail-open/fail-closed behavior, ordering, and canonical revalidation;
- integration tests through `runtimebundle` or `stdhttp` when the plugin depends on session, routing, or stream behavior;
- regression tests for security-sensitive behavior such as redaction, auth, tool blocking, or capture.

Protocol plugins should also include golden wire fixtures, streaming tests, cancellation tests, and fuzz tests for decoders where practical.

## Reference plugins

`internal/plugins/features/REFERENCE_PLUGINS.md` lists reference feature plugins registered by the standard bundle. They are proof plugins, not a license to put product logic into core. Prefer hardening or promoting a reference plugin when it already proves the seam you need.

Current proof areas include first-session auto append, tool policy, workspace guard, traffic transcript/capture, and verifier completion gates.

## Import boundaries

Allowed:

- feature plugin -> `pkg/lipapi`, `pkg/lipsdk`, standard library, narrow external dependencies;
- backend plugin -> provider SDK, `pkg/lipapi`, `pkg/lipsdk`, internal backend-local helpers;
- frontend plugin -> HTTP/wire helpers, `pkg/lipapi`, `pkg/lipsdk`, runtime executor view contracts;
- standard distribution -> concrete bundled plugins and core wiring packages.

Forbidden:

- `internal/core` importing concrete plugins;
- `pkg/lipapi` or `pkg/lipsdk` importing `internal/...` packages;
- provider SDK imports outside backend plugins, refclients, or tests explicitly designed for conformance;
- feature plugins importing executor, routing, or B2BUA internals to bypass SDK seams.

If a plugin cannot be implemented without a forbidden import, document the missing seam before implementing the feature.
