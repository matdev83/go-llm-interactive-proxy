# Custom Compatible Backends

Custom compatible backends let operators add API-compatible providers without writing a Go plugin. Use them when a provider exposes an OpenAI Chat Completions, OpenAI Responses, or Anthropic Messages compatible API but the standard distribution does not ship a dedicated connector.

## Factory kinds

Use the existing backend `kind` field to select the factory and `id` as the runtime route backend instance:

| Factory kind | Upstream API |
| --- | --- |
| `custom-openai-legacy-compatible` | OpenAI-compatible `/chat/completions` |
| `custom-openai-responses-compatible` | OpenAI-compatible `/responses` |
| `custom-anthropic-compatible` | Anthropic-compatible `/v1/messages` |

Each enabled custom backend requires a unique `config.backend_prefix`. The prefix is used for backend inventory and prefixed routing. It must not contain `/` or `:`, must not duplicate another enabled custom backend, and must not use a reserved standard backend prefix such as `nvidia`, `openrouter`, `anthropic`, `openai-legacy`, or `openai-responses`.

## API keys

Custom backends follow the existing static API key convention:

- `api_key`, `api_keys`, and `credentials` in YAML are explicit operator credentials.
- `credentials` take precedence when present because they preserve credential IDs and remote account metadata.
- If YAML credentials are omitted, `api_key_env_var_root` supplies environment fallback keys.
- Numbered environment keys use the standard convention: `ROOT`, then `ROOT_2`, `ROOT_3`, and so on.

For example, `api_key_env_var_root: PROVIDER123_API_KEY` reads `PROVIDER123_API_KEY`, `PROVIDER123_API_KEY_2`, `PROVIDER123_API_KEY_3`, etc.

## Model inventory

By default, custom backends probe the provider for models and register them in the central model registry under `backend_prefix`:

- OpenAI-compatible backends call `<base_url>/models` with bearer authentication.
- Anthropic-compatible backends call `<base_url>/v1/models` with `x-api-key` and `anthropic-version` headers.

Operators can override remote discovery with static `models:` config using the same inline or file inventory shape as other backends.

## Example: New OpenAI-compatible provider

```yaml
plugins:
  backends:
    - id: provider123
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: provider123
        base_url: https://api.provider123.example/v1
        api_key_env_var_root: PROVIDER123_API_KEY
```

With this config, route selectors can target discovered models from the `provider123` backend prefix after model inventory refresh.

## Example: Static inventory override

```yaml
plugins:
  backends:
    - id: provider123
      kind: custom-openai-responses-compatible
      enabled: true
      config:
        backend_prefix: provider123
        base_url: https://api.provider123.example/v1
        api_key_env_var_root: PROVIDER123_API_KEY
        models:
          source: inline
          items:
            - canonical_id: provider123/deepseek-chat
              native_id: deepseek-chat
              display_name: Provider123 DeepSeek Chat
```

## Example: Anthropic-compatible provider

```yaml
plugins:
  backends:
    - id: provider-anthropic
      kind: custom-anthropic-compatible
      enabled: true
      config:
        backend_prefix: provider-anthropic
        base_url: https://api.provider-anthropic.example
        api_key_env_var_root: PROVIDER_ANTHROPIC_API_KEY
```
