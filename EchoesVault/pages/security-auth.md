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
- Standard HTTP startup refuses administrative/root-style execution
- Backend factories declare credential posture so non-local deployments reject unknown or user-OAuth credentials early
- Fail closed at composition or startup boundaries

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

- `internal/pluginreg/keys.go` - supported provider env vars and numbering rules
- `pluginreg.ResolveUpstreamAPIKeysFromEnv` - resolves once at startup
- OpenAI Codex `auth.json` managed-OAuth files must be `0600` (reject group/other-readable)
- Symlinked managed-OAuth account files skipped

## Key Packages

| Package | Responsibility |
|---|---|
| `internal/core/accessmode/` | Access mode definitions |
| `internal/core/auth/` | Authentication logic |
| `internal/core/securesession/` | Session authority, resume, denial |
| `internal/core/safety/` | Safety checks |
| `internal/pluginreg/` | Credential posture metadata |
| `internal/stdhttp/` | Transport auth, security guard |
| `internal/infra/runtimebundle/` | Security policy checks at composition |
| `pkg/lipsdk/auth/` | Auth SDK facades |
| `pkg/lipsdk/transport/` | Transport auth SDK facades |

## Config Security

- `http_client.trust_environment_proxy: false` when environment not trusted
- `auth.access_mode` controls auth enforcement
- `auth.local_api_keys` for multi-user non-loopback deployments
