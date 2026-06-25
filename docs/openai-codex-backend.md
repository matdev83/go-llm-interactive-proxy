# OpenAI Codex backend

The `openai-codex` backend connects to the ChatGPT Codex Responses API (`https://chatgpt.com/backend-api/codex/responses`). Route selectors use the `openai-codex` prefix, for example `openai-codex:gpt-5.3-codex`.

## Enable

```yaml
plugins:
  backends:
    - id: codex
      kind: openai-codex
      enabled: true
      config:
        access_token: ""   # or api_key
        account_id: ""     # optional ChatGPT account header
```

## Credentials

- YAML: `access_token` (preferred) or `api_key`, plus optional `api_keys` / `credentials`.
- Environment: `OPENAI_CODEX_ACCESS_TOKEN`, then numbered `_2`, `_3`, …; falls back to `OPENAI_CODEX_API_KEY` (+ `_N` variants) when access-token vars are unset.
- When neither `access_token` nor `auth_json_path` is set, the connector reads `~/.codex/auth.json` if present (Codex CLI default).

## Optional settings

| Field | Purpose |
| --- | --- |
| `base_url` | Default `https://chatgpt.com/backend-api/codex` |
| `auth_json_path` | Explicit Codex CLI `auth.json` path (overrides default discovery) |
| `refresh_token` | OAuth refresh token when used |
| `oauth_token_url` | OAuth token endpoint (default `https://auth.openai.com/oauth/token`) |
| `oauth_client_id` | OAuth client id (OpenAI Codex CLI default) |
| `account_id` | `ChatGPT-Account-Id` header |
| `default_reasoning_effort` | Default reasoning effort for requests |
| `default_temperature` | Unsupported by Codex; setting it causes requests to fail explicitly |
| `models` | Static model inventory (inline or file), same shape as other backends |
| `managed_oauth_enabled` | Load OAuth accounts from JSON files in `managed_oauth_storage_path` |
| `managed_oauth_storage_path` | Directory of `*.json` account files |
| `managed_oauth_accounts` | Account ids to use; empty or `all` uses every valid file |
| `managed_oauth_selection_strategy` | `first-available` (default), `round-robin`, or `session-affinity` |
| `managed_oauth_session_affinity_ttl_seconds` | Evict session→account bindings after this many seconds (0 = no TTL) |
| `managed_oauth_session_affinity_max_entries` | Max in-memory session affinity entries (0 = unlimited) |
| `managed_oauth_allow_auth_json_fallback` | Fall back to `auth_json_path` / default auth.json when no usable managed accounts |
| `rate_limit_fallback_seconds` | 429 cooldown when `Retry-After` is missing (default 60) |
| `gpt55_downgrade_disabled` | Disable the free-plan `gpt-5.5` downgrade recovery |
| `gpt55_downgrade_source_model` | Source model for downgrade detection (default `gpt-5.5`) |
| `gpt55_downgrade_target_model` | Target model for free-plan downgrade (default `gpt-5.4`) |
| `plan_type_hint` | Optional plan hint for proactive downgrade tests/local overrides |

Without `models`, the connector exposes a built-in Codex model list.

## Client compatibility (OpenCode / Pi / Droid)

OpenCode, Pi, and Factory Droid bridge prompts are **not** applied by the backend adapter. Enable the opt-in feature plugin instead:

```yaml
plugins:
  features:
    - id: codex-client-compat
      enabled: true
      config: {}
```

The request-part hook detects client markers from extensions, headers, prompts, and tool names, then mutates the canonical call for selected `openai-codex` backend attempts before backend translation. It no-ops for other backends.

## Routing example

```yaml
routes:
  default: "openai-codex:gpt-5.3-codex"
```

Bracket parameters such as `?reasoning_effort=high` are supported in route selectors.
