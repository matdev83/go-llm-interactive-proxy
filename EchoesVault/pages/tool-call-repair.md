---
type: architecture
title: Canonical Tool-Call Repair
description: V1 native structured tool-call repair at the canonical event layer (ADR 0007 / issue #152).
stack: [go]
tags: [tools, repair, streaming, finalizer, adr-0007]
status: active
---

# Canonical Tool-Call Repair

V1 repairs **native structured** tool calls only at the canonical tool-event layer. Authoritative design: [docs/adr/0007-canonical-tool-call-repair.md](../../docs/adr/0007-canonical-tool-call-repair.md).

## Ownership

- Core owns bounded per-B-leg buffering on `retryRecvStream`, exact original replay, `Engine.Repair`, and the `toolcall.Finalizer` adapter.
- Effective catalog is `retryRecvStream.baseline.Tools` after filtering/shaping.
- The feature package is YAML-only (`DecodeConfig`); `standardplugins` maps config into the core adapter and contributes `FeatureBundle.ToolCallFinalizationMaxArgsBytes` (merged as the minimum of positive values).
- Adapters do not repair.
- Enablement is registration-row `enabled` only; standard bootstrap injects an enabled config row when absent. Any matching features row suppresses injection; omitting `enabled: true` is an explicit disabled opt-out under plain-bool config semantics.

## V1 rules (compressed)

- Deterministic repairs only; mandatory post-validation.
- Engine outcomes: pass / rewrite / unrepairable (policy maps unrepairable to pass-through or reject).
- Name normalization: lowercase ASCII; strip ASCII whitespace/`_`/`-`; rewrite only on exactly one match.
- Scalar coercion disabled initially.
- Diagnostics and errors never include raw arguments, tool names, schema bodies, instance values, or external-reference URLs. JSON Pointer paths are bounded to 256 runes.
- Shape profiles: request envelope 8 MiB default / depth 128 / duplicates accepted; schema 256 KiB / depth 32 / duplicates rejected; args 64 KiB default / depth 64 / duplicates rejected. HTTP body caps are independent of tool limits. Recursive ordered parse retained after preflight; iterative builder deferred.

## Phase status

Phase 1 froze ADR, fixtures, engine RED contracts, Execute/`retryRecvStream` sequence tests, and the injection helper stub. Phase 2 landed `FeatureBundle.ToolCallFinalizers`, snapshot/runtimebundle wiring, and the core per-B-leg assembler on `retryRecvStream` (hold/pass/rewrite/overflow/cleanup). Phase 3 landed bounded offline JSON Schema compile/validate (`ValidateArgsAgainstSchema`), digest-keyed LRU cache, and immutable catalog indexing in `internal/core/toolcallrepair` (pinned `jsonschema/v6`). Phase 4 landed `Engine.Repair`. Phase 5 packaged `internal/plugins/features/toolcallrepair` as a `toolcall.Finalizer`, registered it in `standardplugins`, and injects an enabled standard-distribution row when absent. Opt-out is any matching features row (by instance ID or factory kind); `enabled: false` or a row without `enabled: true` both suppress injection. Phase 6 added adversarial and secret-leakage regression tests, concurrent cache churn, native fuzz targets, and bounded structured error paths; existing runtime tests prove panic/error fail-open exact replay and reject cleanup. Phase 7 runs the finalizer through every tools-viable bundled frontend/backend conformance cell and gates adapters from importing repair implementation packages. Phase 8 wires `FuzzCompleteJSONSuffix` / `FuzzSchemaPreScanCompile` / `FuzzEngineRepair` into `make test-fuzz`, includes `internal/core/toolcallrepair` in `make bench`, keeps allocation/bound unit checks (no wall-clock CI thresholds), and ships `config/examples/dogfood-tool-call-repair.yaml` for truncated-args dogfood. Shared `internal/core/jsonshape` Token preflight hardens schemas/args before materialization (schema and args reject duplicate names; args depth 64); request envelopes keep historical duplicate acceptance. `RepairContext`/`ValidateContext` propagate `canceled` without payload leakage; `CompleteJSONSuffix` remains append-only completion only. Step 3 coverage locks preflight before every frontend Decode* and before `unmarshalSchemaJSON`/`parseOrderedJSON`/repaired-candidate validation, including `repairArgsJSON` defense-in-depth and archtest call-order gates (`internal/archtest/jsonshape_preflight_order_test.go`). Step 5 retains the recursive ordered JSON builder after preflight depth caps; iterative rewrite is deferred pending material profiling need (see [docs/performance-checks.md](../../docs/performance-checks.md)).
