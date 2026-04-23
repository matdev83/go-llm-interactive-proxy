# Reference feature plugins (stage two)

Optional YAML-enabled examples (non-noop) for each hook family:

| YAML `id` | Family | Purpose |
|-----------|--------|---------|
| `ref-submit-annotate` | submit | Adds `x_lip_ref_submit` JSON extension on the canonical call. |
| `ref-request-suffix` | request/response parts | Appends a suffix to the first user text part; prefixes assistant text deltas. |
| `ref-tool-prefix` | tool reactor | Rewrites tool argument deltas with a configurable prefix. |

Example:

```yaml
plugins:
  features:
    - id: ref-submit-annotate
      enabled: true
      config:
        marker: "my-env"
    - id: ref-request-suffix
      enabled: true
      config:
        suffix: " [staging]"
        response_prefix: "STG:"
    - id: ref-tool-prefix
      enabled: true
      config:
        prefix: ">>"
```

Tool reactor error policy (global hook bus) in root config:

```yaml
hooks:
  tool_reactor_error_policy: fail_open   # or fail_closed, swallow_event
```

## Stage-four reference proof set (extension seams)

Narrow plugins that validate the published stage-four surface (see design §19). Each is registered in the standard bundle.

| YAML `id` | Seams | Purpose |
|-----------|-------|---------|
| `ref-autoappend-file` | session opener, request transform | First-new-session label + append configurable text to the first user text part. |
| `ref-tool-policy` | tool catalog filter, tool reactor | Drop blocked tool defs; swallow tool events for blocked names or prefixes. |
| `ref-workspace-guard` | workspace resolver, request transform, catalog filter, tool reactor | Static workspace view; session state unlocks a gated tool on later requests; heat-tool guard via workspace label. |
| `ref-traffic-transcript` | traffic observer, redactor, raw capture | In-memory transcript and raw log; redacts substrings on the observation path only. |
| `ref-verifier-stub` | completion gate | After successful aux `Collect` with role `verifier`, replaces the completion with a short stream; pass-through if aux is disabled. |
