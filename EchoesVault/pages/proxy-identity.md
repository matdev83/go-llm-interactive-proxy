---
type: reference
title: Proxy Identity
description: A-leg Server and B-leg User-Agent / OpenRouter attribution carriers, modes, allowlist, and exclusions for issue #147.
stack: [go]
tags: [identity, http, openrouter, b2bua, config]
status: active
---

# Proxy Identity

## Scope

Go LIP presents product identity on **selected HTTP carriers only** (not an arbitrary header tunnel):

- A-leg: response `Server` header toward clients (`internal/stdhttp`, stack-wide).
- B-leg: request `User-Agent` toward approved hosted backends (`httpidentity` wrap).
- B-leg OpenRouter only: `HTTP-Referer` and `X-OpenRouter-Title`.

Operator source: [docs/proxy-identity.md](../../docs/proxy-identity.md).
Config comments: [config/config.yaml](../../config/config.yaml).
Policy model: `internal/core/identity`.

## Defaults

Omitting `identity:` defaults to LIP product **proxy** identity (`go-llm-interactive-proxy` /
project GitHub URL). That is an intentional compatibility break from older OpenRouter
client-first / empty-header behavior.

Modes: `proxy` | `passthrough` | `custom` | `drop`. Downstream `server` forbids `passthrough`.

Passthrough User-Agent: call path missing/invalid omits the header; background/inventory
(no call-path client context) uses product identity.

## Allowlist and exclusions

**Eligible:** `openai-responses`, `openai-legacy`, `anthropic`, `gemini`, `bedrock`,
`openrouter`, `nvidia`, `huggingface`.

**Excluded** (vendor/local/OAuth/agent/custom; no LIP identity wrap):
`openai-codex`, `openai-codex-app-server`, `acp`, `cursorcliacp`, `geminicliacp`,
`agycliacp`, `opencode-go`, `opencode-zen`, `ollama`, `ollama-cloud`, `llamacpp`,
`lmstudio`, `vllm`, `local-stub`, `custom-openai-legacy-compatible`,
`custom-openai-responses-compatible`, `custom-anthropic-compatible`.

## B2BUA

Failover and parallel races keep per-attempt B-leg identity isolation. A-leg `Server`
does not change with the winning upstream. Post-output failures do not trigger identity-bearing failover.

## OpenRouter attribution capture

A-leg OpenRouter app headers are captured by OpenAI Responses/Legacy frontends only.
Anthropic/Gemini capture User-Agent but not OpenRouter attribution headers.

## Legacy OpenRouter fields

Prefer nested backend or global `identity.openrouter.app_url` / `app_title`. Backend
`static_referer` / `static_title` override global OpenRouter policy when present
(factory sets `LegacyAppURL` / `LegacyAppTitle` on the connector config; empty
`FieldPolicy.Mode` is not a legacy signal — it means `proxy`). Fail-closed conflict
only with nested backend `identity.openrouter.app_*`. Wire title uses
`X-OpenRouter-Title`, not `X-Title`.

## Implementation notes

- Canonical field: `lipapi.Invocation.ClientUserAgent` (not a nested `ClientIdentity` struct).
- Backend OpenRouter overrides nest under `identity.openrouter.*` (flat `app_url`/`app_title` rejected).
- A-leg uses a commit-time `ResponseWriter` wrapper that preserves `Flusher` / `Unwrap`.
- Passthrough: call-path missing/invalid omits; background/inventory uses product identity.

Scenario registry: [docs/spec-bundle-identity-scenarios.md](../../docs/spec-bundle-identity-scenarios.md).

## Related

- [routing-orchestration.md](routing-orchestration.md)
- [security-auth.md](security-auth.md)
- [architecture-overview.md](architecture-overview.md)
