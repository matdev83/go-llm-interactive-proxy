# Backend plugin authoring guide

This guide covers closed executable backend plugins for Go-LIP. The reference module is [`connectors/localstub`](../../connectors/localstub).

## Closed manifest

Ship a `*.backendplugin.json` next to the install root (discovery does not recurse). Required fields: `schema`, `plugin_id`, `version`, `build_id`, `executable` (relative, no `..`), `sha256`, protocol range, `platforms`, and `exports[]` with `kind`, `credential_mode`, `access_scope`, `process_sharing`.

Template: `connectors/localstub/manifest/template.backendplugin.json`. Packaging fills digest/`build_id` from [`release.yaml`](../../connectors/localstub/release.yaml).

## SDK server helper

Implement `pkg/lipsdk/backendplugin.Service` and serve via `backendplugin.NewGRPCServer` over the host-provided secure channel (`LIP_PLUGIN_CHANNEL_PIPE` on Windows). Do not import root `internal/…`.

## Capabilities and process sharing

Advertise honest static capabilities in `Describe`. Use `process_sharing: per_instance` unless isolation and concurrency are declared for shared artifacts. Route prefixes should identify the factory kind.

## Opaque config ownership

Configuration YAML is opaque to the host and delivered only after peer authentication. Parse and validate inside the connector. Secrets arrive only in `ConfigureRequest.Secrets` after peer auth—never via process environment bootstrap.

## Exact trust and secure IPC

Hosts verify digest, bind a private staged executable, launch that exact image, and authenticate the peer before negotiate/configure. Darwin remains fail-closed in current production profiles.

## Conformance

Run `pkg/lipsdk/backendplugin/conformance.Run` in the connector module (`GOWORK=off go test ./...`).

## Module versioning and release

- Connector `go.mod` may `replace` the monorepo root for local development only (`release.yaml` `replace_policy`).
- Published tags drop `replace` and depend on a tagged `github.com/matdev83/go-llm-interactive-proxy` version.
- Root never requires or replaces connector modules. Root CI uses `GOWORK=off`.

## Installation, compatibility, rollback

Operator install/trust/diagnostics/upgrade/rollback: [`operator.md`](operator.md) (platform dirs, closed manifests, inspect/doctor, troubleshooting).

- Minimal package: root binary only (`make package-minimal`).
- Full package: structurally discovers connectors/*/release.yaml profiles containing full (no maintained connector name list); installs into OS plugin roots (`/opt/go-lip/plugins`, `/Library/Application Support/Go-LIP/plugins`, `%ProgramFiles%\Go-LIP\plugins`) with installer ownership and proxy read/execute metadata (`ACCESS.txt`). Runtime ACL application is layout-tested; live ProgramFiles mutation is not performed in CI.
- Removing one plugin directory leaves unrelated backends. Installed but unconfigured plugins stay inactive (no process launch).

## Private companions

Node/Python bridges stay under the connector package `private/` directory. Hosts must not import or launch them. See `docs/backend-plugins/examples/private-bridge/`.
