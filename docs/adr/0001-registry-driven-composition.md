# ADR 0001: Registry-driven composition for the standard bundle

## Status

Accepted (stage two).

## Context

The standard distribution must stay statically linked while avoiding central `switch` wiring that drifts from `plugins` configuration. Config rows for frontends, backends, and features must map to real constructors and mount paths.

## Decision

- Use `internal/pluginreg` as the **standard bundle registry** (value type [`pluginreg.Registry`](../../internal/pluginreg/reg.go)): factories are installed with **`pluginreg.InstallStandardBundleOn(reg)`** (per-surface tables in `standard_table.go` / `*_install.go`). The standard binary (`cmd/lipstd`) owns a dedicated `reg := pluginreg.NewRegistry()`, installs the bundle on `reg`, validates mandatory ids, and passes `reg` through **`runtimebundle.BuildOptions.PluginRegistry`** and stdhttp entrypoints. Tests follow the same rule: **`pluginreg.InstallStandardBundleOn(r)`** on a fresh [`pluginreg.NewRegistry`](../../internal/pluginreg/reg.go), then inject `r` into build/mount options. There is **no** separate `internal/standardbundle` implementation package; [`internal/pluginreg/standardbundle`](../../internal/pluginreg/standardbundle/install.go) documents the explicit bundle only. The mandatory id list lives in `pkg/lipsdk.StandardDistributionRequirements`.
- Composition roots (`cmd/lipstd`, `internal/stdhttp`) resolve plugins **only** through registry APIs (`BuildBackend`, `MountFrontend`, `BuildFeatureHooks`, etc.) on the registry instance they were given. **`internal/infra/runtimebundle`** assembles enabled backends (using a shared upstream `*http.Client` from `internal/infra/httpclient` unless tests inject one), continuity store, and the core executor—including optional **routing health** (circuit breaker from `routing.health.circuit_breaker`) and **route observation** (structured `lip.route` logs when a logger is present).
- `pkg/lipsdk` holds stable **registration and factory contracts**; bundled plugins implement those contracts without being imported by `internal/core`. HTTP frontends share **`internal/plugins/frontends/execerr`** to map executor errors to HTTP status (reject vs internal) without duplicating classification.

## Consequences

- Adding a bundled plugin requires a registry entry and config documentation; no new switch arms in the core.
- Duplicate ids must be rejected at registration time (see registry validation tasks).
- Operator-facing behavior for routing cooldown and observability is configured in YAML (`routing.health`, executor logging) and documented in the main README “Current state” section and [`docs/routing-health-circuit-breaker.md`](../routing-health-circuit-breaker.md).
- Architecture budgets and import guardrails are enforced in [`internal/archtest`](../../internal/archtest/guardrails_test.go) and ADR 0005.
