# Plugin authoring guide

This guide explains how to write feature and protocol plugins that preserve the Go proxy architecture. For the complete stage map, see `docs/extension-points.md`. For operator configuration and examples, see `README.md` and `config/config.yaml`. For YAML-only OpenAI/Anthropic-compatible provider wiring, see [`docs/custom-compatible-backends.md`](custom-compatible-backends.md). For the **no-key local stub** maintainer workflow (`check-config`, routes, inventory, serve), see [`docs/dogfood-local.md`](dogfood-local.md).

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
- `pkg/lipsdk/auxiliary` for controlled sub-calls;
- `pkg/lipsdk/secretguard` for opaque ingress secret-guard contracts (`Guard`, `Matcher`, `DecisionEvent`).

## Standard feature: secrets-guard

`secrets-guard` is a bundled standard feature (factory kind and plugin id `secrets-guard`) registered in `internal/standardplugins/`. It contributes one or more `secretguard.Guard` values on `feature.FeatureBundle.SecretGuards`, executed at stage id **`secret_guard`** immediately after `securesession.BeginTurn` and before frontend ingress checkpoint, traffic capture, submit hooks, and routing. Only one enabled `secrets-guard` feature instance is supported in v1; multiple enabled registrations must fail startup.

Authoring and extension rules:

- Feature code lives in `internal/plugins/features/secretsguard/` and must not import runtime, frontends, or backends.
- Catalog construction and Aho–Corasick matching live in `internal/core/secretsguard/`; runtime composition and inventory projection live in `internal/infra/runtimebundle/`; audit delivery adapters live in `internal/infra/secretaudit/`.
- SDK consumers receive an opaque **`Matcher` / `MatcherResolver`** via `secretguard.Services`. No API exposes raw catalog values or accepts an environment reader at request time. The opaque matcher belongs only in middleware request context; `AuthenticationResult` carries safe attribution targets only.
- In **`single_user`**, composition loads proxy credential env vars (bare + sparse numbered), a curated popular-env registry, and operator `include_env` / `exclude_env` hints at startup only. The loaded catalog is a startup snapshot; credential rotation requires restarting all replicas and verifying the refreshed catalog after restart.
- In **`multi_user`**, composition selects a request-credential matcher with **zero** process-environment reads, even when `single_user.*` YAML is present (startup rejects that key in multi-user mode). Device/key/fingerprint values are attribution-only and are not scanned as secret catalog entries.
- Enabled configuration requires explicit `action`: `block`, `redact`, or `log`. Disabled feature creates no catalog, no stage work, and no behavior change.
- On match, the runtime emits a dedicated **`secretguard.DecisionEvent`** audit record (safe fields only). Do not overload `policydecision.Record`. Optional `proxy_instance_id` / `pod_id` attribution is future work, not v1.
- The shared declared public-prefix registry is value-based, not env-name based: prefix provenance comes from the loaded secret value, not from the environment-variable name that sourced it.

Operator configuration and examples: [`docs/secrets-guard.md`](secrets-guard.md), [`config/config.yaml`](../config/config.yaml), and [`config/examples/`](../config/examples/).

## Standard feature: reasoning-output-preservation

`reasoning-output-preservation` is a bundled standard feature (plugin id `reasoning-output-preservation`, issue #157) registered in `internal/standardplugins/`. On the standard `lipstd` bootstrap path it is injected enabled (`action: restore`, `use_builtin_catalog: true` / `compatible-auto.v2`) when no matching features row exists; explicit matching `enabled: false` (or omitted enabled) opts out and constructs no participants. It is not a mandatory `StandardDistributionRequirements` entry. Installed/enabled does not mean universally active — capture and restore require catalog/rule eligibility; unmatched candidates are inert. When active it contributes an attempt transform and a final-stream observer factory for bounded process-local reasoning capture/restore. Feature code lives in `internal/plugins/features/reasoningpreservation/` and must not import runtime, frontends, or backends. Shared matcher: `internal/reasoningreplay`. Operator configuration and examples: [`docs/reasoning-output-preservation.md`](reasoning-output-preservation.md), [`config/config.yaml`](../config/config.yaml), [`config/examples/reasoning-preservation-observe.yaml`](../config/examples/reasoning-preservation-observe.yaml), [`config/examples/reasoning-preservation-restore.yaml`](../config/examples/reasoning-preservation-restore.yaml), and the [release checklist](reasoning-output-preservation-release-checklist.md).

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

When authoring protocol/frontend adapters for tool use, name assistant tool-call/function-call arguments explicitly in coverage tables and fixture comments; do not bury them under generic "message JSON" language.

## Reference plugins

`internal/plugins/features/REFERENCE_PLUGINS.md` lists reference feature plugins registered by the standard bundle. They are proof plugins, not a license to put product logic into core. Prefer hardening or promoting a reference plugin when it already proves the seam you need.

Current proof areas include first-session auto append, tool policy, workspace guard, traffic transcript/capture, verifier completion gates, and ingress secrets guard.

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
