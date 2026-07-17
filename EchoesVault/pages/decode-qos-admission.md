---
title: Decode QoS Admission
type: concept
tags: [frontends, qos, http, config]
---

# Decode QoS Admission

Standard `lipstd` / `stdhttp` mounts share one decode admission limiter across all frontends.

## Defaults (after Validate)

| Setting | Default |
| --- | --- |
| `server.max_request_body_bytes` | handler default **8 MiB** when omitted |
| `server.max_concurrent_decodes` | **32** |
| `server.max_inflight_decode_bytes` | **64 MiB** |
| `server.max_pending_wire_events` | **0** (unlimited; not normalized) |

Decode admission zeros (`max_concurrent_decodes`, `max_inflight_decode_bytes`) normalize to the **finite** defaults above during `config.Validate` (and `EffectiveMax*` helpers). They do **not** mean unlimited. `max_pending_wire_events` is independent: **0 = unlimited** and is never rewritten by Validate/Build. Negative values are invalid. `max_inflight_decode_bytes` must be **≥** the effective max request body so one max-sized request can admit. Operators raising the request body for large multimodal / long-context (for example toward 1M–2M-token envelopes) must raise **both** `max_request_body_bytes` and `max_inflight_decode_bytes` together. Overall HTTP request-body caps are independent of tool-schema / tool-args `jsonshape` limits.

## Handler order

1. `reqbody.ReadAll` — decompressed absolute size cap → **413** when over. Body bytes are already fully resident in memory after this step.
2. JSON preflight (`jsonguard` / `jsonshape` request profile)
3. `DecodeAdmission.TryAcquire(len(body))` — weighted bytes + concurrency for **protocol Decode only** (does not cover body read or preflight)
4. Body-touching route extraction (`FromModelOrDefault`) and protocol adapter Decode under admission (released before Execute / stream)

## 413 vs 429

- **413 Request Entity Too Large**: decompressed body exceeds `max_request_body_bytes` / handler default. Hard absolute cap.
- **429 Too Many Requests** + `Retry-After: 1`: temporary decode admission saturation (concurrency slots or weighted in-flight byte budget). Overweight vs configured budget also maps to 429 under validation-backed configs; keep byte budget ≥ body cap so overweight is not the normal path.

## Wiring

- `lipsdk.FrontendMountOptions.DecodeAdmission` carries a tiny `TryAcquire` interface (SDK must not import `internal/`).
- `internal/plugins/frontends/decodeqos.Limiter` implements it.
- `runtimebundle.Build` installs one finite limiter on `Built.DecodeAdmission`; `stdhttp` mounts that shared singleton into every enabled frontend.
- Nil admission disables limiting (custom/manual mounts only). Pending wire queues on the executor use configured `MaxPendingWireEvents` as-is (**0 = unlimited**).

## Inventory

`server_limits` on the diagnostics inventory snapshot exposes the four numbers only (no payloads). Decode fields are effective (zero→finite defaults); pending wire is as configured.

## Future

Streaming / spool-backed decode may avoid holding a full decompressed copy before Decode; today weight is `len(body)` after ReadAll and admission does not cover the ReadAll copy cost.
