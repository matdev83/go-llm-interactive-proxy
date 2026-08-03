# Generic OpenResponses compatible backend

The `custom-openresponses-compatible` backend mode is the built-in generic remote
OpenResponses backend. It is pinned to the OpenResponses `2026-04-24` profile
(`profile: "2026-04-24"` is the only supported value). Route selectors use the
configured `backend_prefix`, for example `prefix:my-model`.

It is an essential built-in compatible mode (origin `built_in_compatible`), not
an external connector: it never launches a plugin process and never accepts
literal YAML credentials (use `api_key_env_var_root` or omit it for no-auth
endpoints).

## Enable

```yaml
plugins:
  backends:
    - id: ors
      kind: custom-openresponses-compatible
      enabled: true
      config:
        backend_prefix: ors
        base_url: https://api.provider.example/v1
        api_key_env_var_root: ORS_API_KEY
```

Config surface: `backend_prefix`, `profile`, `base_url`,
`api_key_env_var_root`, `models`, `capabilities`, `dialects`,
`request_limits`, `response_limits`. Provider-connector-owned controls
(OpenRouter attribution, routing, billing, provider-specific request controls)
are rejected at load time.

## Capability surface

The `capabilities` list is the honest, portable feature claim for the pinned
profile. When omitted, the generic mode claims:

`streaming`, `tools`, `vision`, `documents`, `reasoning`,
`parallel_tool_calls`, `ordered_items`, `assistant_phase`, `item_references`,
`compaction`, `opaque_extensions`.

The following capabilities are **not** part of the default surface and require
an explicit declaration to claim:

- `video_input` — inline video references (`input_video`) are representable on
  the pinned profile, but only forward when the operator declares them.
- `reasoning_replay` — replaying historical reasoning parts is a hard capability
  declared when a call carries reasoning output.

Declaring the full default surface plus any of those is valid.

## Removed / rejected capabilities

Two capability names are **not representable on the pinned `2026-04-24`
profile** and cannot be claimed at all:

- `annotations` — the pinned content-part schema has no annotation carrier
  (`InputTextContentParam`/`InputFileContentParam` define no `annotations`
  field), so the generic mode cannot satisfy the capability end-to-end.
- `assistant_media_refs` — the pinned profile has no assistant-media response
  surface, so the generic mode cannot deliver assistant media references.

Consequences for operators:

1. `annotations` has been **removed from the default capability list**. A
   generic OpenResponses backend never advertises it.
2. Declaring either name in `capabilities:` is **rejected at config load** with
   an error, so operators cannot make dishonest capability claims that would
   later fail or silently degrade.

```yaml
# Rejected at load time:
capabilities: [streaming, tools, annotations]
# Rejected at load time:
capabilities: [streaming, tools, assistant_media_refs]
```

Calls that genuinely need these semantics must route to a backend that can
represent them (for example a native OpenAI Responses backend or an external
connector with a response media surface). Content parts the generic mode cannot
encode (annotations, assistant media references, summaries, reasoning parts,
and other unrepresentable canonical kinds) are rejected **before any upstream
request** — never silently text-mapped or dropped.

## Pinned content-part profile

The generic mode encodes canonical message content to the pinned `2026-04-24`
content parts:

- text → `input_text` / `output_text`
- images → `input_image` (`image_url`)
- files → `input_file` with `filename`, `file_data` (base64, ≤ 32 MiB), and
  `file_url`
- videos → `input_video` (`video_url`)
- vendor-prefixed custom parts (containing `:` or `/`) → preserved opaquely

Fields the pinned profile does **not** define are rejected rather than dropped:

- `input_file.file_id` — not in `InputFileContentParam`; a non-null value is
  rejected before canonical construction.
- `input_video.video_data` — not in `InputVideoContent`; a non-null value is
  rejected before canonical construction.

Explicit `null` for these fields is treated as absent.
