# Backend plugin operator guide

Install, trust, diagnose, upgrade, roll back, and remove **executable** optional backend connectors without rebuilding `cmd/lipstd`. Hybrid composition is [ADR 0008](../adr/0008-hybrid-backend-connector-plugins.md). Authoring details remain in [`authoring.md`](authoring.md).

**Trust posture (operator):** an installed connector runs as a separate process behind approved local IPC and digest checks. That **process isolation is not a malicious-code sandbox**. Treat installed plugins as **trust-equivalent to code you chose to run as the proxy service account**. The dedicated threat model and accepted controls are in [`threat-model.md`](threat-model.md) (`make backend-plugin-security-checks`).

There is **no runtime download** of plugins. Operators (or installers) place artifacts on disk; Go-LIP only discovers and launches what is already present under trusted roots.

## Package layouts: minimal vs curated-full

| Profile | Contents | Typical use |
|---|---|---|
| **minimal** | Standard `lipstd` binary layout only — **no** optional connector executables | Essentials-only deployments; add plugins later |
| **curated-full** | Structurally discovers every `connectors/*/release.yaml` whose `profiles` include `full` (not a maintained name list) and stages digests + manifests | Dogfood, offline bundles, curated appliance images |

Commands (repo root):

```bash
make package-minimal PACKAGE_DEST=.golip-package-staging/minimal
make package-full PACKAGE_DEST=.golip-package-staging/full
make package-plugin-smoke
go run ./tools/backendplugin/package_plugins -profile full -dest .golip-plugins/full
```

Each staged tree includes `ACCESS.txt` (ownership posture metadata) and `package-index.json`. See fixtures [`examples/operator/package-index.minimal.json`](examples/operator/package-index.minimal.json) and [`examples/operator/package-index.full.json`](examples/operator/package-index.full.json).

## Platform install directories and permissions

Default **machine-scoped** plugin roots (installer/admin owned; proxy account **read + execute** only):

| Platform | Default plugin root |
|---|---|
| Linux | `/opt/go-lip/plugins` |
| macOS | `/Library/Application Support/Go-LIP/plugins` |
| Windows | `%ProgramFiles%\Go-LIP\plugins` |

Guidance:

- **Linux/macOS:** directories `0755` or tighter; plugin executables `0755` (or `0555`); manifests `0644`. Prefer root/admin ownership; the proxy UID/GID should not be able to rewrite digests or executables.
- **Windows:** Administrators (or installer SID) modify; service/user SID **Read & execute**. Avoid granting the proxy account write/modify on the plugin tree. Named-pipe ACL enforcement is host-owned; do not widen pipe DACLs for convenience.
- Packagers may inject another installation-owned default (for example `/usr/libexec/go-lip/plugins`); the runtime never guesses mutable per-user locations unless **`development_mode`** is on with **explicit** `paths`.

Copy one plugin directory (manifest + `bin/` + digest metadata) into a trusted root. Removing that directory uninstalls that artifact; other plugins remain. No root Go rebuild required.

## Discovery, trust, and closed manifests

```yaml
plugins:
  backend_discovery:
    enabled: true
    paths:
      - /opt/go-lip/plugins
    strict: true
    development_mode: false
  backends: []
```

| Field | Operator meaning |
|---|---|
| `enabled` | When false, optional connectors are not discovered |
| `paths` | Explicit trusted roots (non-recursive install directories containing `*.backendplugin.json`) |
| `strict` | Fail closed on discovery/layout errors |
| `development_mode` | Allows only the explicit `paths` you list; still **no** implicit home-directory plugin root |

**Closed manifest:** unknown JSON fields fail closed. Required shape matches [`examples/operator/closed-manifest.backendplugin.json`](examples/operator/closed-manifest.backendplugin.json):

```json
{
  "schema": "golip.backendplugin.manifest/v1",
  "plugin_id": "io.golip.backend.localstub",
  "version": "0.1.0",
  "build_id": "REPLACE_BUILD_ID",
  "executable": "bin/lip-backend-localstub",
  "sha256": "REPLACE_SHA256",
  "protocol_major": 1,
  "protocol_min_minor": 0,
  "protocol_max_minor": 0,
  "platforms": [{"os": "linux", "arch": "amd64"}],
  "exports": [
    {
      "kind": "local-stub",
      "credential_mode": "none",
      "access_scope": "any",
      "process_sharing": "per_instance"
    }
  ]
}
```

**Digest / exact artifact / private staging:** the host verifies `sha256`, binds a **private staged** copy of the executable, and launches those exact bytes — not a mutable path-only trust. Staging is cleaned on shutdown/upgrade paths exercised by packaging smoke tests.

**Configured-missing:** an enabled backend whose kind is not built-in and not discovered fails closed at composition (inspect/check-config surface the gap). Installed but **unconfigured** plugins stay inactive (no process launch).

Validated configs:

- [`config/examples/plugin-operator-minimal.yaml`](../../config/examples/plugin-operator-minimal.yaml) — discovery off / essentials + optional stub after single-plugin install
- [`config/examples/plugin-operator-full-discovery.yaml`](../../config/examples/plugin-operator-full-discovery.yaml) — curated-full discovery path
- [`examples/operator/discovery-development.yaml`](examples/operator/discovery-development.yaml)
- [`examples/operator/discovery-production.yaml`](examples/operator/discovery-production.yaml)

## Approved IPC, peer auth, and secrets

- Hosts use **approved secure local IPC** profiles (platform-specific; Windows uses a host-provided pipe such as `LIP_PLUGIN_CHANNEL_PIPE`). Unauthorized local peers cannot negotiate/configure.
- **Peer-authentication failure:** doctor/configure stops; **connector credentials are never sent** after channel/peer failure.
- Secrets arrive only in authenticated configure payloads — not via unprotected process-environment bootstrap. Do not put provider API keys into plugin launch env.
- **Local-only** connectors (`access_scope` / local-only posture) are rejected when `access.mode: multi_user`. Keep them on single-user loopback deployments.

## Inspect and doctor

```bash
go run ./cmd/lipstd check-config --config CONFIG
go run ./cmd/lipstd inspect --config CONFIG
go run ./cmd/lipstd doctor --config CONFIG --instance INSTANCE_ID
```

| Command | Launches plugins? | Meaning |
|---|---|---|
| `check-config` | No | Validates YAML + composition readiness |
| `inspect` | No | Built-in vs discovered kinds, versions, conflicts, configured-missing, activation needed |
| `doctor --instance ID` | **Only that** configured instance | Handshake / secure-channel / peer checks; never all discovered plugins |

Inspect states operators commonly see: discovered, configured, missing kind, manifest invalid, digest mismatch, builtin collision, local-only rejected. Doctor failures on peer/channel leave no credential exposure to the plugin.

## Compatibility, upgrade, rollback, uninstall

**Compatibility:** host and plugin negotiate protocol major/minor from the manifest. Incompatible major versions fail before configure.

**Atomic upgrade:**

1. Stage the new artifact beside the live tree (or into a versioned directory).
2. Verify digest/`package-index.json`.
3. Atomically replace the published plugin directory (packaging uses staging + publish).
4. Point discovery at the new root if you use versioned roots — see [`config/examples/plugin-operator-upgrade.yaml`](../../config/examples/plugin-operator-upgrade.yaml) and [`examples/operator/upgrade-candidate.yaml`](examples/operator/upgrade-candidate.yaml):

```yaml
plugins:
  backend_discovery:
    enabled: true
    development_mode: true
    strict: true
    paths:
      - .golip-plugins/upgrade-candidate/localstub
```

5. `check-config` + `inspect`; optional `doctor --instance …`.

**Rollback:** keep the previous published directory; retarget `backend_discovery.paths` (or restore the prior atomic publish) — [`config/examples/plugin-operator-rollback.yaml`](../../config/examples/plugin-operator-rollback.yaml), [`examples/operator/rollback-previous.yaml`](examples/operator/rollback-previous.yaml). No `lipstd` rebuild.

```yaml
plugins:
  backend_discovery:
    enabled: true
    development_mode: true
    strict: true
    paths:
      - .golip-plugins/previous/localstub
```

**Uninstall / cleanup:** delete the plugin install directory; remove or disable its `plugins.backends` rows. Locked source artifacts and private staging must not remain after tested shutdown/upgrade (packaging smoke covers staged cleanup). Unrelated plugins keep working.

## Routing Execution Composition Policy

By default, Go-LIP applies a **safe** routing execution composition policy (`routing.execution_composition_policy: safe`). Under this policy:
- Backends classified as `agent_runtime` (such as ACP agent connectors, Cursor SDK agents, or OpenAI Codex App-Server) and backends with `unknown` execution class **cannot** be mixed into composite routing selectors (failover `|`, parallel/race `!`, weighted `^`, or thinker hybrid chains) with other backends.
- Direct routing to any backend (e.g. `acp:claude-3-7-sonnet`) is always permitted.
- Pure inference composition (e.g. `openai:gpt-4o|anthropic:claude-3-5-sonnet`) is fully permitted.

To explicitly permit mixed agent runtime and inference composition at operator risk, set:

```yaml
routing:
  execution_composition_policy: unrestricted
```

> [!WARNING]
> In `unrestricted` mode, failover or parallel execution against agent runtimes may trigger duplicate side-effects (e.g. tool execution, file edits, git commands) across multiple backends or retries.

## Troubleshooting

| Symptom | Likely cause | Action |
|---|---|---|
| Unsafe execution composition error | Mixed `agent_runtime` / `unknown` backend in composite route selector | Use direct routing for agent runtimes, or set `routing.execution_composition_policy: unrestricted` if intended |
| Kind missing in inspect | Artifact not under trusted `paths`, or discovery `enabled: false` | Install manifest+bin; fix `paths`; re-run inspect |
| Unknown field / invalid manifest | Closed schema violation | Fix manifest; unknown keys are rejected |
| Digest mismatch | File rewritten after package | Re-package; do not hand-edit binaries |
| Peer/channel failure in doctor | IPC/ACL/profile mismatch | Fix install permissions/ACLs; do not disable peer checks |
| Configured-missing fail-closed | Enabled backend kind not discovered | Install artifact or disable the row |
| Local-only rejected | `access.mode: multi_user` | Single-user loopback or different connector |
| Development path ignored | `development_mode: false` with only loose paths | Set `development_mode: true` **only** for explicit lab paths |
| Wanted “download plugin” | Unsupported | Package offline; copy artifacts — **no runtime download** |

## Related

- [`authoring.md`](authoring.md) — connector authors
- [`docs/dogfood-local.md`](../dogfood-local.md) — no-key stub workflow
- [`EchoesVault/pages/backend-connector-plugins.md`](../../EchoesVault/pages/backend-connector-plugins.md)
