# Cursor SDK backend (experimental)

The `cursorsdk` backend is an **experimental**, separately routable Cursor connector delivered as the external module `connectors/cursorsdk` (manifest-discovered executable plugin + `bridge-node` private companion over exact `@cursor/sdk` **1.0.23**). It does **not** replace `cursorcliacp`. ACP remains independently selectable. There is no connector-local fallback between the two.

Operator sample: [`config/examples/cursor-sdk-experimental.yaml`](../config/examples/cursor-sdk-experimental.yaml). Companion bridge notes: [`connectors/cursorsdk/bridge-node/README.md`](../connectors/cursorsdk/bridge-node/README.md).

## Status

- Experimental and **non-default**. Not part of `lipsdk.StandardDistributionRequirements` or `EssentialBackendBundle`.
- Delivered only as `connectors/cursorsdk` (closed manifest + digest-bound executable). Root-static registration is forbidden.
- Local-only (`BackendAccessLocalOnly` / `access_scope: local_only`): single-user loopback only. Rejected under `access.mode: multi_user`.
- Future default switch or ACP deprecation requires a separate reviewed migration; this feature retains both connectors.

## Install the bridge (manual)

Go-LIP **never** runs `npm install`, `npm ci`, or other package lifecycle scripts during config validation, startup, model discovery, or request handling. Operators (or CI) install the companion package separately.

Prerequisites:

| Requirement | Value |
| --- | --- |
| Node | `>=22.13` |
| `@cursor/sdk` | exact `1.0.23` (pinned in the bridge `package.json` / lockfile) |
| Platforms | Linux x64/arm64, macOS x64/arm64, Windows x64. Version 1.0.23 has no Windows arm64 package. |

From the repo:

```bash
cd connectors/cursorsdk/bridge-node
npm ci
npm run typecheck
npm test
npm run build
```

Put `lip-cursor-sdk-bridge` on `PATH`, or set `bridge_executable` to the built binary / `bin/lip-cursor-sdk-bridge.js` entry. Lookup is direct PATH/absolute resolution only — no shell, no `npm` launcher names.

Exact version checks (no credentials, no agents):

```bash
node bin/lip-cursor-sdk-bridge.js --version   # must report @cursor/sdk 1.0.23
node bin/lip-cursor-sdk-bridge.js doctor
```

## API key and billing

SDK auth is a **static Cursor SDK API key**, separate from Cursor CLI login, Cursor Desktop login, and `cursorcliacp` auth state. Do not treat those as substitutes. Billing for the SDK key is Cursor’s SDK/API billing surface, not the CLI subscription login path.

Precedence:

1. Explicit YAML `api_key` (wins).
2. Else composition-root env `CURSOR_API_KEY` only (bare name; **no** `CURSOR_API_KEY_2` / `_N` numbering).
3. Missing key → actionable config error; secret values are redacted from errors.

The key is sent only in a private bridge request frame after handshake — never on argv or in the child process environment.

## Registration and route selection

- Kind / route prefix: `cursorsdk`.
- Canonical model IDs: `cursor/<native-id>` (same vendor namespace as ACP).
- Registry provenance keeps backend instance and kind distinct from `cursorcliacp`.
- When both connectors are configured, routes must identify the intended backend. A bare model name alone must not silently pick SDK vs ACP.
- Example selectors: `cursorsdk:cursor/<model>`, `cursorcliacp:cursor/<model>`.
- Pre-output failures follow the operator-configured core routing plan only; the adapter never falls back to the other Cursor connector.

## Configuration reference

| Key | Default | Notes |
| --- | --- | --- |
| `api_key` | `CURSOR_API_KEY` | Required when enabled |
| `bridge_executable` | `lip-cursor-sdk-bridge` | Direct executable; shell/npm names rejected |
| `model` | inventory | Optional default model hint |
| `default_workspace` / `workspace_path` / `project_dir` | none | Explicit workspace required per agent identity |
| `mcp_servers` | empty | YAML/JSON object, normalized, max 256 KiB |
| `setting_sources` | `[]` | Empty = none. Allowed: `project`, `user`, `team`, `mdm`, `plugins`, `all` |
| `sandbox_mode` | `required` | `required` or explicit `off` |
| `auto_review` | `false` | Independent of sandbox/trust |
| `bridge_env_allowlist` | platform minimum | Extra names only; credential env names rejected |
| `max_agents` | `32` | 1–32 |
| `max_concurrent_runs` | `8` | 1–8 and ≤ `max_agents` |
| `bridge_start_timeout_seconds` | `30` | 1–120 |
| `cancel_timeout_seconds` | `5` | 0.1–30 |
| `shutdown_timeout_seconds` | `10` | 1–120 |
| `agent_idle_timeout_seconds` | `900` | `0` disables idle eviction; else 1s–24h |
| `models` | live discovery | Optional static inventory override (existing shape) |

### Settings and sandbox

- Default `setting_sources: []` — no ambient Cursor project/user/team settings until explicitly trusted.
- Default `sandbox_mode: required` — fails closed if the SDK reports sandbox unavailable.
- Windows x64 may need explicit local-only `sandbox_mode: off` when sandbox support is unavailable; never silently downgraded.
- `auto_review` defaults off and is not a substitute for sandbox or workspace trust.
- `enableAgentRetries` is forced false inside the bridge; custom tools / `local.force` are not exposed.

### MCP

- Pass configured MCP servers as a YAML/JSON object under `mcp_servers`.
- Values are normalized to deterministic JSON; non-objects and oversize configs fail closed.
- First delivery does **not** bridge Go-LIP tools into the SDK via custom tools, local callbacks, or an implicit LIP↔MCP bridge.
- SDK-native tools and configured MCP run inside the Cursor agent; they are **not** replayed as canonical frontend tool calls.

## Capabilities and limits

Omitted (fail negotiation / reject per core policy) unless later proven:

- canonical tools and parallel tool calls
- vision, documents, structured outputs

Also:

- `EnforcesMaxOutputTokens: false` — max-output requests fail closed rather than silently truncating.
- Reasoning effort maps only through exact catalog-driven SDK equivalents (`reasoning` values, or `effort` via variants that enable thinking). No `xhigh` ↔ `extra-high` aliasing. Boolean-thinking-only models do not advertise canonical reasoning effort.

Frozen bounds: bridge frame 16 MiB; prompt 8 MiB; MCP 256 KiB; retained stderr 8 KiB.

## Streaming, cancel, restart, continuity

- Streaming is primary; canonical order is response start → message start → text/reasoning deltas → conservative per-turn usage → one terminal → EOF.
- Cancel: provider `run.cancel` first; timeout/unresponsive bridge escalates to current-generation process-tree termination.
- Bridge crash/restart invalidates all bridge-local agent/run handles; later work rebuilds from the **canonical** transcript.
- Continuity is **process-local**. SDK `Agent.resume` is not used across Go-LIP or bridge restarts.
- Post-output failures surface on the committed B-leg; no silent retry/switch after first client-visible content.

## Platform matrix and support

| Lane | What it proves |
| --- | --- |
| Default Go tests | Fake bridge / fixtures; no Node, npm, account, or network |
| `make test-cursor-sdk-platform` | Current-OS fake-bridge lifecycle (start/stream/cancel-terminal/crash/restart/rebootstrap/shutdown); no API key |
| `.github/workflows/cursor-sdk-platform.yml` | Linux / macOS / Windows matrix (fake bridge) |
| `make test-cursor-sdk-live` | Opt-in real `@cursor/sdk` Node scenarios only (no Go process lifecycle hooks) |
| `make test-cursor-sdk-live-bridge` | Opt-in Go→Node lifecycle via production `Open`/`RunStream` (`-tags=cursorsdk_live_bridge`, `go test -v` JSON summary); pin/discovery/content/cancel/shutdown; hard-restart/rebootstrap often honest **blocked** live; ordinary `go test` never selects it |

Missing Node, old Node, or unpinned bridge → probe reports **blocked**, not a silent pass. Experimental rollout evidence is accepted; measured live/comparative dogfood remains opt-in blocked without credentials.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Bridge not found | Install/build companion; set `bridge_executable` or PATH; confirm no shell/`npm` wrapper |
| Wrong SDK version | `lip-cursor-sdk-bridge --version` / `doctor` must show `@cursor/sdk` 1.0.23 |
| Node too old | Upgrade to Node ≥ 22.13 |
| Missing API key | Set `api_key` or `CURSOR_API_KEY` (not CLI login) |
| Sandbox unavailable | Prefer fixing sandbox; local-only explicit `sandbox_mode: off` on Windows when required |
| Multi-user rejection | Expected for local-only `cursorsdk` |
| Model-only route ambiguity | Use `cursorsdk:…` or `cursorcliacp:…` explicitly |
| Auth / capability / config errors | Non-recoverable; not treated as transient process faults |

Safe diagnostics expose backend kind/instance, bridge/SDK versions, discovery state, and bounded counts — not keys, prompts, paths, or raw SDK IDs.

## Live tests (opt-in)

```bash
# Platform (no key):
make test-cursor-sdk-platform

# Live Node scenarios (explicit opt-in; no Go kill/restart hooks):
CURSOR_SDK_LIVE=1 CURSOR_API_KEY=... make test-cursor-sdk-live

# Live Go→Node bridge lifecycle (explicit opt-in + build tag; paid/network):
CURSOR_SDK_LIVE=1 CURSOR_API_KEY=... make test-cursor-sdk-live-bridge
```

Rules:

- Without `CURSOR_SDK_LIVE=1` or without `CURSOR_API_KEY`, scripts exit **0** with `BLOCKED` (safe skip; not a green live proof).
- Opted-in Node live prints a JSON summary with `status`: `complete` | `blocked` | `failed` and `ok` true only for `complete`. Incomplete/blocked required scenarios (e.g. missing hard-restart hooks, sandbox unavailable) set `ok: false`; CLI exits nonzero (`3` blocked, `1` failed).
- `test-cursor-sdk-live-bridge` runs `go test -v` under `-tags=cursorsdk_live_bridge` so the harness emits one parent JSON summary on stdout. Parent `status` may be `blocked` with process exit **0** when required live proofs are honestly unavailable (not a silent pass). Builds/resolves the bridge bin, verifies `@cursor/sdk` **1.0.23** via `--version`, launches `node` + `bridge/bin/lip-cursor-sdk-bridge.js` directly (no shell/npm), keeps the API key out of argv/child env (Windows script may copy User/Machine into Process only for the run), and drives production `backendRuntime.Open` → `RunStream`.
- Observed credentialed Windows live (sanitized aggregates only): Node probe passed pin/count/deltas/dispose/cancel; Node core discovery/text/safety_off/configured_mcp/cancel/shutdown passed; reasoning skipped without thinking-delta; sandbox-required blocked; Node hard-restart/rebootstrap hooks blocked. Reuse last credentialed observation (before text-only reuse prompt update) timed out and **has not been revalidated** afterward — do **not** claim Node reuse live-passed. Go live-bridge (`go test -v` JSON): parent may be `status=blocked` exit 0; pin/discovery/canonical single terminal/cancelled terminal/no later content/shutdown passed; hard restart blocked because active text-only peers could not be held; rebootstrap/MCP instrumentation blocked; sandbox-required blocked. Do **not** treat hard restart, rebootstrap, or Node reuse as live-passed.
- Canonical full-prompt rebootstrap is **passed** only with create-count/prompt-capture instrumentation (fake/`make test-cursor-sdk-platform` lane). Fast provider finish before a cancellable in-flight state is **blocked**, not a pass.
- Per-scenario temp workspaces, strict timeouts, isolated state.
- Do **not** collect prompts, tool content, raw workspace paths, keys, or SDK agent/run IDs in artifacts.
- Live suites are separate from default `make test` / CI unit lanes (`cursorsdk_live_bridge` tag is required for the Go harness entrypoint).

## ACP vs SDK comparison report

Repeatable dogfood matrix methodology: [`docs/cursor-sdk-comparison-report.md`](cursor-sdk-comparison-report.md).

```bash
make test-cursor-sdk-comparison-report   # tests + synthetic/blocked Markdown on stdout; no credentials
```

Offline runs label cells `synthetic` or `blocked` and keep `replacement_status: retain_both_connectors`. Measured comparative dogfood stays blocked until opted-in credentials/platform lanes supply a safe aggregate input. This does **not** change defaults or deprecate `cursorcliacp`.

## Minimal enable sketch

```yaml
routing:
  default_route: "cursorsdk:cursor/<model-from-inventory>"

plugins:
  backends:
    - kind: cursorsdk
      id: cursor-sdk
      enabled: true
      config:
        # api_key omitted → CURSOR_API_KEY
        bridge_executable: lip-cursor-sdk-bridge
        default_workspace: /absolute/path/to/workspace
        setting_sources: []          # default: none
        sandbox_mode: required       # Windows may need explicit off (local-only)
        auto_review: false
        mcp_servers: {}              # optional configured MCP object
```

Keep `cursorcliacp` enabled only when you intentionally route to it with an explicit `cursorcliacp:…` selector.
