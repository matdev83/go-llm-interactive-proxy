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
