---
type: reference
title: Security and Auth
description: Startup security posture, transport authentication, secure sessions, and credential management.
stack: [go]
tags: [security, auth, sessions, credentials]
status: active
---

# Security & Auth

## Startup Posture

- `no_auth` is for explicit loopback single-user operation only
- `single_user` is the default and permits only explicit loopback binds; `multi_user` requires config opt-in plus `lipstd serve --multi-user`
- Standard HTTP startup refuses administrative/root-style execution
- Backend factories declare credential and access posture so multi-user deployments reject unknown, user-OAuth, and local-only connectors early
- Local-only connectors include ACP/process-spawning backends, OpenAI Codex, and Codex App Server
- Fail closed at composition or startup boundaries
- Optional executable backend plugins: trust-equivalent to proxy-account code (not a malicious sandbox); see [backend-connector-plugins](backend-connector-plugins.md) and [`docs/backend-plugins/threat-model.md`](../../docs/backend-plugins/threat-model.md)

## Transport Authentication

- Auth/access modes defined in `internal/core/accessmode/`
- Principal attachment at HTTP edge via `internal/stdhttp/`
- API keys must be at least 16 Unicode code points after trimming (non-loopback)
- Diagnostics, pprof, metrics, model-catalog diagnostics require shared secret beyond loopback

## Secure Sessions

- `securesession.BeginTurn` runs before backend execution and client-visible output
- Client-provided session IDs are hints until proxy-owned state validated
- Resume tokens and session IDs are security-sensitive wire values
- Session denial maps to protocol-legal, client-safe errors with operator diagnostics preserved
- Secure-session recording augments B2BUA lineage

## Credential Management

- `internal/standardplugins/keys.go` - supported provider env vars and numbering rules
- `standardplugins.ResolveUpstreamAPIKeysFromEnv` - resolves once at startup
- OpenAI Codex `auth.json` managed-OAuth files must be `0600` (reject group/other-readable)
- Symlinked managed-OAuth account files skipped

## Secrets Guard (issue #151)

Optional ingress feature (`plugins.features` id `secrets-guard`, disabled by default) that scans model-bound textual/JSON fields for **exact loaded secret values** before metering checkpoint, traffic capture, routing, or backend dispatch. It is ingress-only, not response/egress scanning, and only one enabled instance is supported per deployment.

| Topic | Behavior |
|---|---|
| Stage | `secret_guard` after `BeginTurn`; see `docs/secrets-guard.md` |
| Actions | `log` (audit only), `redact` (sanitize call), `block` (quarantine session) |
| Single-user sources | Proxy env vars + popular env (exact names plus uppercase `*_API_KEY`/`*_TOKEN`, excluding frontend public prefixes and underscore-delimited `CSRF`/`XSRF`/`CRSF` segments) + operator include/exclude at startup; snapshot-only (rotation requires restart + post-restart verification) |
| Multi-user sources | Current request credential only plus safe attribution identifiers; **zero** process env reads |
| Audit | Dedicated `secretguard.DecisionEvent`; never raw secrets or prompt excerpts |
| Peer IP | `RemoteAddr` host; forwarded headers ignored |
| Rollout | One action per deployment: disabled -> `log` -> `redact` -> `block` |
| Scan limits | `block` / `redact` scan-limit hits return the normal block path and quarantine the session; `log` continues |
| Future | Optional `proxy_instance_id` / `pod_id` attribution is future work, not v1 |

Registered in `internal/standardplugins/standard_table.go` as feature id `secrets-guard`. Key packages: `pkg/lipsdk/secretguard`, `internal/core/secretsguard`, `internal/plugins/features/secretsguard`, `internal/proxycredentials`, `internal/infra/osenv`, `internal/stdhttp/auth` (opaque matcher in middleware request context), `internal/infra/runtimebundle` (secret-guard composition), `internal/infra/secretaudit` (audit delivery), `internal/core/securesession` (quarantine).

## Key Packages

| Package | Responsibility |
|---|---|
| `internal/core/accessmode/` | Access mode definitions |
| `internal/core/auth/` | Authentication logic |
| `internal/core/securesession/` | Session authority, resume, denial |
| `internal/core/safety/` | Safety checks |
| `internal/pluginreg/` | Credential and access-scope posture metadata |
| `internal/stdhttp/` | Transport auth, security guard |
| `internal/infra/runtimebundle/` | Security policy checks at composition |
| `pkg/lipsdk/auth/` | Auth SDK facades |
| `pkg/lipsdk/transport/` | Transport auth SDK facades |
| `pkg/lipsdk/secretguard/` | Opaque ingress secret-guard contracts (`Guard`, `Matcher`, `DecisionEvent`) |
| `internal/core/secretsguard/` | Catalog, matcher, source policy |
| `internal/plugins/features/secretsguard/` | Feature plugin (`block`/`redact`/`log` Guard) |
| `internal/infra/secretaudit/` | Secret-decision audit delivery (structured log sink) |

## Config Security

- `http_client.trust_environment_proxy: false` when environment not trusted
- `access.mode` controls deployment access posture
- `auth.local_api_keys` for multi-user non-loopback deployments

## Runtime reload management

Separate from data-plane auth. Management HTTP is disabled unless `LIP_RELOAD_MANAGEMENT_ADDRESS` is set. Dedicated `LIP_RELOAD_MANAGEMENT_TOKEN` (≥16 Unicode code points) is required for multi-user or non-loopback; single-user loopback may use local trust. Cookie and data-plane local API keys never authorize reload. See [runtime-config-reload](runtime-config-reload.md).
