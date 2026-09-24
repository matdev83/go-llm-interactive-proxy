# Phase 7 execution evidence

Status: APPROVED

## Scope

Implemented parent specification tasks 7.1-7.4: a negotiated, versioned V2
economic sideband/finalization ABI for executable backend connectors, bounded
host/adapter preservation and draining, an explicit V1 compatibility bridge,
and one reusable connector conformance TCK.

The V2 path is host-only. It does not become canonical client content, does
not perform stream-time rating, journal I/O, token-ledger writes, generic
reconciliation, public host binding, cutover, or real-provider evidence
producer migration (the latter belongs to Phase 8). Existing V1 fields and
field numbers remain unchanged.

## TDD evidence

RED was established before the new SDK ABI and durable coverage carrier
existed:

```text
go test -count=1 ./pkg/lipsdk/backendplugin -run '^TestPhase7EconomicABI_'
go test -count=1 ./internal/core/billing -run 'TestCallLegEconomicDisposition'
```

Result: expected compile failure for the new V2 DTO, conversion, capability,
frame, and call-leg disposition symbols. The adapter replay/conflict and
old-host empty-V2 compatibility tests were likewise added before their
bounded source implementations.

GREEN after the minimal ABI, conversion, negotiation, adapter, host, and
terminal-drain implementation:

```text
go test -count=1 ./internal/core/execbackend ./internal/core/runtime ./internal/infra/backendplugins/adapter ./pkg/lipsdk/backendplugin/...
```

Result: all packages passed, including the V2 ABI and conversion tests, open/
receive/terminal/cancel forwarding tests, adapter replay/conflict tests,
finalizer preservation, neutral core terminal ownership, and the reusable
conformance package.

## Implementation evidence

- `api/backendplugin/v1/backend.proto` adds the negotiated
  `accounting_evidence_v2` capability and typed, host-only V2 sideband and
  finalization payloads. New messages use new tags at the end of the schema;
  the canonical `buf generate --template buf.gen.yaml` command regenerated
  `backend.pb.go`.
- `pkg/lipsdk/backendplugin/convert_economics_v2.go` and the related conversion
  helpers enforce presence versus explicit zero, exact decimal/fraction
  bounds, typed component/dimension/observation limits, UTF-8/control/safe
  diagnostic text, unknown-wire-field rejection, and bounded payload sizes.
- `pkg/lipsdk/backendplugin/economics_v2.go`, `bounds.go`, `protocol.go`, and
  `types.go` make V2 capability and minor-version negotiation explicit. V2 is
  required before a strict lane proceeds; an old connector can use
  `LiftAccountingEvidenceV1`, which emits only representable token measures
  with `partial` coverage and never invents a charge.
- `pkg/lipsdk/backendplugin/forward_execute*.go`, `frames.go`, `server.go`,
  and `host/session.go` carry V2 only on the negotiated host sideband. V2
  evidence is drained on open failure, receive, normal terminal, cancellation,
  and close through the existing attempt/connector ownership path.
- `internal/infra/backendplugins/adapter/{economic_evidence.go,stream.go,backend.go}`
  preserve source-separated measures, subject/correlation/provider identity,
  coverage and supersession, revisions/stream sequence, provenance and
  lifecycle metadata. Exact frame+finalizer replay is idempotent; a conflicting
  same-identity payload remains retained and returns a typed conflict.
- `internal/core/execbackend/backend.go` and the narrow runtime files provide a
  neutral handoff to Phase 5 terminal ownership. The core does not import the
  connector wire package; economic evidence never produces a client event.
- `internal/core/billing/economic_evidence.go` and the additive fields on
  `CallLegUsageRecord` persist a bounded, versioned disposition carrier keyed by
  observation identity/hash. Complete, partial, unsupported and safe reasons
  survive terminal append, JSON storage/replay, and conflict fingerprints
  without changing the provider observation or turning metadata into a meter.
- `internal/infra/billingstore/phase7_economic_disposition_test.go` and
  `internal/core/runtime/phase7_economic_terminal_test.go` prove V1-lifted
  partial evidence, native complete evidence, unsupported evidence, legacy V1
  row compatibility, durable replay, and visible same-observation disposition
  conflicts.
- `pkg/lipsdk/backendplugin/conformance/{economic_cases.go,cases.go}` adds a
  reusable V1/V2 TCK with synthetic image, audio, video, document, gauge,
  exact decimal, charge graph/coverage, late revision, replay/conflict,
  old/new host, size-limit, strict negotiation, and redacted-error fixtures.

## Generator and reproducibility

Canonical generation was run from the API module:

```text
Set task-scoped TEMP/TMP/GOTMPDIR and Go caches under E:\\codex-phase7-connector-abi
Set-Location api
buf generate --template buf.gen.yaml
```

A second run produced no additional tracked changes. The generated
`api/backendplugin/v1/backend.pb.go` SHA-256 was
`0EA9DDCE0F83B59EA70248AED2EBDFCFD6C5E595BA5750902163207909873653`.
`backend_grpc.pb.go` remained unchanged.

## Acceptance mapping

| Task | Evidence |
| --- | --- |
| 7.1 versioned V2 ABI | New capability, sideband/finalizer messages, preserved V1 schema, canonical generated output, strict DTO/protobuf validation, and reproducibility check. |
| 7.2 host/adapter preservation and drains | Adapter buffer plus neutral runtime seam preserve identity, source, charge/coverage/supersession, revisions, provenance, lifecycle, and safe lexemes; forwarding tests exercise open failure, receive, terminal, cancel, and host-only behavior. Replay/conflict tests prove one charge for exact duplicates and visible conflict for changed same-identity evidence. |
| 7.3 negotiation and V1 bridge | Capability/minor checks are explicit; strict unsupported evidence fails before execution; optional empty V2 drains remain compatible with old hosts while non-empty unsupported V2 fails closed; V1 lifting is limited to representable counters and reports partial coverage; durable coverage dispositions survive terminal/storage replay and conflicts; unknown fields, types, components, monetary detail, sizes, and unsafe diagnostics fail closed. |
| 7.4 reusable conformance | Shared TCK covers V1/V2 presence/zero, exact fractions/decimals, multimodal direction/unit/quality, charge graph, gauges, revisions, replay/conflict, old/new hosts, size bounds, and redacted errors with synthetic non-token media cases. |

## Verification

Passed:

```text
go test -count=1 ./internal/core/execbackend ./internal/core/runtime ./internal/infra/backendplugins/adapter ./pkg/lipsdk/backendplugin/...
go test -count=1 ./internal/core/billing -run 'TestCallLegEconomicDisposition'
go test -count=1 ./internal/infra/billingstore -run 'TestSQLiteCallLegEconomicDispositionRoundTripAndConflict'
go test -count=1 ./internal/core/runtime -run 'TestPhase7EconomicRuntimeTerminalRetainsV1PartialDisposition|TestPhase7EconomicRuntimeTerminalDispositionConflictIsVisible'
go test -count=1 ./pkg/lipsdk/backendplugin -run 'TestPhase7EconomicForward'
go vet ./pkg/lipsdk/backendplugin/... ./internal/infra/backendplugins/adapter/... ./internal/core/execbackend ./internal/core/runtime
go test -count=1 ./internal/archtest -run '^TestCriticalFileBudgets$'
go test -count=1 ./internal/archtest -run 'TestBackendPluginSecurity_makefileAndCIWired|TestBackendPluginCrossPlatform|TestBackendPluginReleaseGates_makefileAndCIWired|TestCoreExcludesBackendPluginHostAndWire|TestPublicBackendPluginABI_NoInternalOrProviderSDKs|TestBackendPluginABI_LegacyAllowlistOnly|TestBackendPluginABI_ProtoFieldsScanned|TestPublicBackendPluginABIBaselineIsExact'
make parity-checks
gofmt -l check on 45 touched Go files (no output; generated output was checked by buf reproducibility)
git diff --check
```

`make parity-checks` passed for the contract TCKs, backend-plugin conformance,
compatible providers, connector-support/acp, and all optional connector
modules. The focused connector-support/openai-compatible module tests also
passed with `GOWORK=off`.

## Skipped or baseline failures

- `go test -race` for the touched backendplugin/adapter/runtime trees could
  not build because the installed Windows `cgo.exe` exited with status 2 from
  the task-scoped E: Go toolchain cache; no race result is claimed.
- `make quality-checks` passed formatting and generated-feature checks but
  stopped at the repository's existing `go mod tidy` check. The ignored
  leading-space directory ` internal/infra/billingstore` causes the malformed
  import-path error; it was not modified or removed.
- The full `./internal/archtest` suite still reports branch-wide baseline
  failures for request/attempt-state ratchets, the pre-existing `Rater`
  declaration, relative-temp source scanning with E: `GOTMPDIR`, the malformed
  ignored directory, and the aggregate internal/core complexity budget.
  Focused ABI/security/core-exclusion and critical-file-budget guards pass.
- A later broad `./internal/core/runtime` rerun also hit the known intermittent
  `TestBlocker1_WireOwnershipTransfer_CancelALegPromptlyCancelsBackendStreamAndSingleTerminalOutcome`
  stale-winner assertion; the isolated Phase 7 runtime tests pass, and no
  Phase 7 assertion is involved in that baseline failure.
- Full `make test`/`make qa` was not rerun because this phase is scoped to the
  connector ABI and its existing host/terminal drains; the targeted tests,
  vet, architecture guards, and parity suite are the relevant replacements.

## Files changed

API and generated transport:
`api/backendplugin/v1/README.md`, `api/backendplugin/v1/backend.proto`,
`api/backendplugin/v1/backend.pb.go`.

Architecture baselines/guards:
`internal/archtest/abi_go_structural_test.go`,
`internal/archtest/abi_structural_test.go`,
`internal/archtest/extension_architecture_guards_test.go`,
`internal/archtest/testdata/backend_plugin_go_abi_baseline.json`.

Neutral core terminal handoff:
`internal/core/execbackend/backend.go`,
`internal/core/runtime/attempt_session.go`,
`internal/core/runtime/attempt_usage_evidence.go`,
`internal/core/runtime/billing_collector.go`,
`internal/core/runtime/billing_leg.go`,
`internal/core/runtime/executor_open_attempt.go`,
`internal/core/runtime/executor_settlement.go`,
`internal/core/runtime/phase7_economic_terminal_test.go`,
`internal/core/runtime/response_pipeline_observations.go`,
`internal/core/runtime/turn_terminal.go`.

Durable coverage carrier and persistence tests:
`internal/core/billing/economic_evidence.go`,
`internal/core/billing/economic_disposition_test.go`,
`internal/infra/billingstore/phase7_economic_disposition_test.go`.

Connector adapter:
`internal/infra/backendplugins/adapter/backend.go`,
`internal/infra/backendplugins/adapter/economic_evidence.go`,
`internal/infra/backendplugins/adapter/phase7_economic_adapter_red_test.go`,
`internal/infra/backendplugins/adapter/phase7_economic_finalizer_test.go`,
`internal/infra/backendplugins/adapter/stream.go`.

SDK ABI, host, bridge, conversions, and tests:
`pkg/lipsdk/backendplugin/bounds.go`, `pkg/lipsdk/backendplugin/conformance/cases.go`,
`pkg/lipsdk/backendplugin/conformance/economic_cases.go`,
`pkg/lipsdk/backendplugin/convert_descriptor.go`,
`pkg/lipsdk/backendplugin/convert_economics_v2.go`,
`pkg/lipsdk/backendplugin/convert_frames.go`,
`pkg/lipsdk/backendplugin/convert_server_frames.go`,
`pkg/lipsdk/backendplugin/economics_v2.go`,
`pkg/lipsdk/backendplugin/forward_execute.go`,
`pkg/lipsdk/backendplugin/forward_execute_active.go`,
`pkg/lipsdk/backendplugin/frames.go`,
`pkg/lipsdk/backendplugin/host/session.go`,
`pkg/lipsdk/backendplugin/phase7_economic_abi_red_test.go`,
`pkg/lipsdk/backendplugin/phase7_economic_forward_test.go`,
`pkg/lipsdk/backendplugin/protocol.go`,
`pkg/lipsdk/backendplugin/server.go`,
`pkg/lipsdk/backendplugin/types.go`,
`pkg/lipsdk/backendplugin/v1_economic_bridge.go`,
`pkg/lipsdk/backendplugin/v1_economic_bridge_test.go`,
`pkg/lipsdk/metering/ports.go`.

## Residual risks

- Durable call-leg records now retain coverage dispositions in an additive,
  versioned JSON carrier; legacy V1 rows remain readable with the carrier
  absent. Later reconciliation/rating work still owns interpretation of that
  metadata, not its persistence.
- Real provider evidence producers remain unchanged by design and must be
  migrated in Phase 8.
- Race verification remains unavailable due the Windows cgo/toolchain failure;
  no source race claim is made.

No Kiro status or task checkboxes were changed.

Root review is APPROVED; see `phase7-review.md`.
