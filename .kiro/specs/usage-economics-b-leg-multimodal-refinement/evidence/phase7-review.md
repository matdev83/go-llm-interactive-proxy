# Phase 7 review

- Verdict: APPROVED.
- Scope: parent tasks 7.1–7.4.
- Boundary: executable connector V2 economic ABI, negotiation, host drains, V1 compatibility, durable coverage disposition, and reusable conformance. Real provider migrations and later rating/reconciliation remain out of scope.

## Requirement review

- The protobuf change is additive: V1 field numbers and meanings remain unchanged, V2 has a dedicated negotiated feature/minor version, and canonical generation is reproducible.
- DTO conversions preserve absence versus explicit zero and exact decimals while bounding messages, observations, components, dimensions, evidence fields, diagnostics, and unsafe or unknown protobuf content.
- Connector V2 observations remain host-only, drain on every execution/finalization exit, and never become canonical client events. Exact replay deduplicates; changed same-identity evidence is retained as a visible conflict.
- Strict required-feature negotiation fails before provider execution. Optional empty V2 sources remain compatible with old peers; actual unnegotiated V2 payloads fail closed before provider content is forwarded.
- The V1 bridge maps only representable token fields, labels coverage partial, and does not invent provider charges or full V2 support.
- Complete, partial, and unsupported coverage dispositions plus safe reasons survive the neutral runtime handoff and durable call-leg JSON/replay identity. Legacy V1 rows remain unchanged.
- The reusable TCK covers presence, exact decimals, multimodal units and quality, gauges, charge coverage, revisions, replay/conflict, bounds, negotiation, and secret-safe errors.

## Fresh root verification

- Backendplugin SDK/conformance/host, connector adapter, execbackend, billing, runtime, and billingstore focused suites — PASS except one broad runtime invocation reproduced the documented unrelated wire-cancellation flake.
- Isolated Phase 7 runtime, durable disposition, forwarding, V1 bridge, and ABI tests — PASS.
- `go vet` across touched SDK, adapter, core, runtime, billing, and billingstore trees — PASS.
- `git diff --check` — PASS.

The worker additionally reported connector parity and targeted ABI/security guards passing. Windows race testing remains unavailable because `cgo.exe` fails during build. No Phase 7 requirement-blocking finding remains.
