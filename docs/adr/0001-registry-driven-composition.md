# ADR 0001: Registry-driven composition for the standard bundle

## Status

Accepted (stage two).

## Context

The standard distribution must stay statically linked while avoiding central `switch` wiring that drifts from `plugins` configuration. Config rows for frontends, backends, and features must map to real constructors and mount paths.

## Decision

- Use `internal/pluginreg` as the **standard bundle registry**: register factories by plugin id at `init` time.
- Composition roots (`cmd/lipstd`, `internal/stdhttp`) resolve plugins **only** through registry APIs (`BuildBackend`, `MountFrontend`, `BuildFeatureHooks`, etc.).
- `pkg/lipsdk` holds stable **registration and factory contracts**; bundled plugins implement those contracts without being imported by `internal/core`.

## Consequences

- Adding a bundled plugin requires a registry entry and config documentation; no new switch arms in the core.
- Duplicate ids must be rejected at registration time (see registry validation tasks).
