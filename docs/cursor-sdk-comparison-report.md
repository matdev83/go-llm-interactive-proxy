# Cursor ACP vs SDK comparison report (methodology)

This document defines the repeatable **ACP (`cursorcliacp`) versus SDK (`cursorsdk`)** dogfood matrix. It does **not** switch defaults or deprecate ACP. `cursorsdk` stays experimental and non-default until a separate reviewed migration proposal meets Requirement 12 gates.

## Run offline (no credentials)

```bash
make test-cursor-sdk-comparison-report
```

This runs schema/redaction tests and prints a bounded Markdown report from the **synthetic/blocked** fixture. It never requires `CURSOR_API_KEY` or network.

Optional:

```bash
go run ./internal/plugins/backends/cursorsdk/comparison/cmd/report -format=json
go run ./internal/plugins/backends/cursorsdk/comparison/cmd/report -format=markdown -out=temp
go run ./internal/plugins/backends/cursorsdk/comparison/cmd/report -fixture=./safe-input.json
```

Do **not** commit generated report artifacts. Use stdout or a temp file. The docs example path above is relative on purpose; absolute paths are rejected in report inputs.

## Matrix dimensions

| Dimension | What it compares |
| --- | --- |
| `setup` | Install/config/startup failure posture |
| `inventory` | Model discovery reliability |
| `ttft` | Time-to-first-token distribution |
| `completion_latency` | End-to-end completion latency |
| `pre_output_failures` | Failures before first client-visible content |
| `post_output_failures` | Failures after committed output |
| `cancellation` | Cancel completion / timeout escalation |
| `restart` | Bridge/process restart recovery |
| `leaks` | Orphan agent/process/goroutine incidents |
| `continuity` | Canonical transcript rebuild / continuity failures |
| `platform_defects` | OS/arch-specific defects |
| `upstream_update_maintenance` | Cost of SDK/CLI pin updates |

Every report must include **both** connectors × **all** dimensions.

## Evidence classes

| Class | Meaning |
| --- | --- |
| `measured` | Opted-in dogfood or CI lane with real samples (`samples >= 1`) |
| `synthetic` | Offline scaffold only (`samples=0`, **no** rate/latency/count metrics) |
| `blocked` | Lane not run (`samples=0`, **no** metrics; `blocked_reason` enum required) |

Default offline reports mix `synthetic` and `blocked` with no numeric comparative metrics. Markdown renders `samples=0` as `-`. They **must not** claim measured superiority.

## Allowed input fields (schema v1)

Top-level: `schema_version`, `generated_at`, `cells`.

Per cell:

- `connector`: `cursorsdk` | `cursorcliacp`
- `dimension`: one of the matrix keys above
- `evidence_class`: `measured` | `synthetic` | `blocked`
- `aggregates`: `samples` required; optional metrics (`rate`, `p50_ms`, `p95_ms`, `count`, `max_live`, `duration_ms`) only on `measured` cells
- `incident_class`: bounded enum (`none`, `startup_failure`, `pre_output_failure`, …)
- `blocked_reason`: enum (`sdk_live_opt_in_required`, `acp_dogfood_lane_not_opted_in`, `measured_input_not_provided`)
- `note`: enum (`offline_scaffold`, `awaiting_opt_in`) or empty

Unknown JSON fields are rejected. Input ≤64KiB; report JSON ≤256KiB.

## Forbidden content

Inputs and reports must **never** include:

- prompts or reasoning text
- tool arguments / tool results
- raw workspace paths
- API keys or credential material
- SDK agent/run IDs

The loader scans for forbidden keys and secret/path/ID patterns before decode.

## Replacement posture

Reports always set `replacement_status: retain_both_connectors` for this methodology. A future default switch or `cursorcliacp` deprecation requires a separate reviewed change with migration docs, rollback, and a compatibility window.

## Limitations

Actual comparative dogfood remains **blocked** until operators opt into live SDK scenarios (`CURSOR_SDK_LIVE=1` + `CURSOR_API_KEY`) and an intentional ACP comparison lane, then supply a safe measured input document. Offline synthetic/blocked output is a methodology scaffold, not migration evidence.
