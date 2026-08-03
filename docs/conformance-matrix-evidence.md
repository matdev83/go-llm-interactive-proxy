# FE×BE conformance matrix — test evidence

The bundled matrix is the Cartesian product of [`BundledFrontendIDs()` and `BundledBackendIDs()`](../internal/testkit/conformance/matrix.go) (authoritative 5×9 = 45 cells: five frontends × nine backend compatibility identities, including OpenResponses, OpenRouter, and NVIDIA). Subset rules (ACP tools/multimodal, OpenRouter/NVIDIA tools/multimodal) and the cell driver classification live in [`newCell`](../internal/testkit/conformance/matrix.go). OpenRouter/NVIDIA are authoritative compatibility identities driven through the actual connector executables via the backendplugin host adapter: [`DeployConnectorColumnFor`](../internal/testkit/conformance/openresponses_provider_mode.go) and the generalized connector-host harness ([`connector_host.go`](../internal/testkit/conformance/connector_host.go)) build and launch the real `connectors/openrouter` / `connectors/nvidia` processes, so connector-specific headers, credentials, inventory, and error behavior have executable matrix evidence ([`connector_columns_matrix_test.go`](../internal/testkit/conformance/connector_columns_matrix_test.go)). The connectors remain optional modules (never essential backend kinds). Per-cell feature evidence for all 45 cells lives in [`matrix_evidence_45.go`](../internal/testkit/conformance/matrix_evidence_45.go).

## How cells are covered (iteration model)

Instead of one test function per cell, the conformance package **iterates `AllCells()`** and runs the same scenario per `openai-responses__anthropic`-style subtest.

| Subset | Matrix filter | Primary tests (all use `for _, cell := range AllCells()` + `t.Run(frontend__backend)`) |
|--------|----------------|----------------------------------------------------------------------------------------|
| **Text** | `TextViable` (always true today) | `TestConformance_TextOnly_roundTrip`, `TestConformance_TextOnly_streamAndNonStreamParity`, `TestConformance_TextOnly_upstreamErrorShape` in [`conformance_text_test.go`](../internal/testkit/conformance/conformance_text_test.go) |
| **Text (credential pool)** | `TextViable` | `TestConformance_CredentialPool_TextOnly_*` in [`backend_credentials_test.go`](../internal/testkit/conformance/backend_credentials_test.go) |
| **Tools** | `ToolsViable` | `TestConformance_Tools_roundTripAndUsage` in [`conformance_tools_test.go`](../internal/testkit/conformance/conformance_tools_test.go) — **excludes** FE×`acp` because [`SubsetMeta`](../internal/testkit/conformance/matrix.go) sets `ToolsViable: false` for ACP |
| **Tool-call repair** | `ToolsViable` | `TestConformance_ToolCallRepairCanonicalMatrix` uses truncated-args refbackends ([`NewToolCallRepairRefBackend`](../internal/testkit/conformance/tools_repair_refbackend.go)) and asserts closed repaired args; gemini cells skip (wire materializes args as objects) |
| **Multimodal** | `MultimodalViable` | `TestConformance_Multimodal_imageInUpstream`, `TestConformance_Multimodal_pdfInUpstream` in [`conformance_multimodal_test.go`](../internal/testkit/conformance/conformance_multimodal_test.go) — image/file URI references project to ACP resource prompt blocks, so FE×`acp` is viable; the generic OpenResponses backend represents inline file_data losslessly as pinned `input_file`, so document cells are positive there too; unrepresentable forms (video/audio) reject before network |
| **Multimodal (credential pool)** | `MultimodalViable` | `TestConformance_CredentialPool_Multimodal_*` in [`backend_credentials_test.go`](../internal/testkit/conformance/backend_credentials_test.go) |
| **Dual-plane economic** | `DualPlaneEconomicCells()` (FE × stream/nonstream/protocol_error/cancel/encoding_failure) | `TestDualPlaneEconomic_*` in [`dual_plane_economic_conformance_test.go`](../internal/testkit/conformance/dual_plane_economic_conformance_test.go); customer FE quantity equivalence in [`phase71_cross_protocol_economic_conformance_red_test.go`](../internal/core/runtime/phase71_cross_protocol_economic_conformance_red_test.go) |

## Explicit exceptions (no extra tests required beyond matrix meta)

| Cells | Restriction | Reference |
|-------|-------------|-----------|
| All frontends × **`acp`** | Tools disabled by design (reject before network); multimodal image/file URI references project to ACP resource prompt blocks, so the multimodal subset is exercised | [`matrix.go` `SubsetJustification`](../internal/testkit/conformance/matrix.go) |

Parity suite files per protocol id remain as in [`conformance-golden-coverage.md`](conformance-golden-coverage.md).

## Drift guard

[`matrix_evidence_test.go`](../internal/testkit/conformance/matrix_evidence_test.go) asserts conformance sources still use the `AllCells()` iteration pattern for text/tools/multimodal tiers.

## OpenResponses frontend compatibility row (spec Phase 8, Task 8.3)

The OpenResponses frontend row is the nine-cell feature-aware classification of
the OpenResponses frontend against every bundled backend (legacy OpenAI Chat,
OpenAI Responses, ACP, Anthropic, Gemini/Vertex, Bedrock, OpenResponses-compatible,
OpenRouter, NVIDIA). It is a strict subset of the authoritative `AllCells()` 5×9
matrix (Task 8.5).

- Row evidence registry: [`openresponses_frontend_row.go`](../internal/testkit/conformance/openresponses_frontend_row.go)
- Row evidence validator (default build): [`openresponses_frontend_row_evidence_test.go`](../internal/testkit/conformance/openresponses_frontend_row_evidence_test.go)
- Row cell scenarios (`//go:build integration`): [`openresponses_frontend_row_conformance_test.go`](../internal/testkit/conformance/openresponses_frontend_row_conformance_test.go)
- Row executable scenario table (`openresponses_frontend_row_scenarios.go`) and its table-driven executor (`openresponses_frontend_row_scenarios_test.go`, `//go:build integration`)
- OpenRouter/NVIDIA connector-column route proof: [`openresponses_provider_mode.go`](../internal/testkit/conformance/openresponses_provider_mode.go) and the connector-host harness [`connector_host.go`](../internal/testkit/conformance/connector_host.go)

Every cell feature is classified lossless / documented_deterministic_projection /
rejected_before_network / out_of_scope with linked scenario IDs and test artifacts;
every rejection asserts zero upstream requests. OpenRouter/NVIDIA remain optional
connector columns (not in the essential backend table) and are proven through the
actual connector executables (connector host).

Scenario IDs are derived from the **row executable scenario table**: the tagged
table-driven executor consumes [`OpenResponsesFrontendRowScenarios`](../internal/testkit/conformance/openresponses_frontend_row_scenarios.go)
directly, and the evidence builder links the exact same IDs. Every row cell owns a
dedicated `-continuation`, `-cancellation`, `-failover`, and `-no-retry` scenario so
evidence never points at a generic `-json-text` or `-usage-commitment` proof:

| Row feature | Executable proof (per row cell) | Outcome |
|---|---|---|
| JSON text / instructions / history | JSON round trip, exact text, exactly one upstream request (ACP ≥ 1) | lossless / projection (ACP, connector-column cells) |
| SSE text | SSE round trip over the same canonical stream | lossless / projection |
| Tools | raw tool create; ACP rejects with zero requests, others project to the wire | projection / lossless (openai-responses, openresponses) / reject (ACP, openrouter, nvidia) |
| Multimodal | raw image create with upstream projection; ACP projects to resource blocks | projection / lossless / reject (openrouter, nvidia) |
| Usage/errors | upstream 500 → stable client-visible error envelope, upstream attempt | lossless / projection |
| Continuation | proxy-owned continuation: second `store:true` create with `previous_response_id` re-executes through the same projector; non-ACP cells assert exactly one additional upstream request; the **ACP v1 prompt-turn subset cannot replay a materialized trajectory that carries the prior ACP reasoning output, so the ACP continuation call is honestly rejected before network with zero additional upstream requests** | lossless / rejected_before_network (ACP) |
| Cancellation/backpressure | blocking origin + client cancel → upstream stops, candidate untouched (candidate receives zero requests) | lossless / projection |
| Failover | failing primary + succeeding candidate; both origin counts asserted | lossless / projection |
| No-retry after visible output | mid-stream-death origin emits first content then dies; candidate receives zero requests | lossless / projection |
| Reasoning replay / phase / item refs / compaction / extensions | raw unrepresentable wire form → rejected with zero upstream requests (OpenResponses backend cell: the positive `-compaction` scenario round-trips compaction losslessly and the positive `-itemref` scenario round-trips item references losslessly) | rejected_before_network (lossless for compaction and item references on the openresponses cell) |
| Assistant media output | the OpenResponses frontend has no EventAssistantImageRef/EventAssistantFileRef output mapping, so no assistant media reference output surface exists in the row configuration | out_of_scope |

## OpenResponses backend compatibility column (spec Phase 8, Task 8.4)

The OpenResponses backend column is the five-cell classification of the generic
OpenResponses-compatible backend against every bundled frontend family (OpenAI
Chat, OpenAI Responses, Anthropic, Gemini, OpenResponses). It is a strict subset
of the authoritative `AllCells()` 5×9 matrix (Task 8.5).

- Column evidence registry: [`openresponses_backend_column.go`](../internal/testkit/conformance/openresponses_backend_column.go)
- Column evidence validator (default build): [`openresponses_backend_column_evidence_test.go`](../internal/testkit/conformance/openresponses_backend_column_evidence_test.go)
- Column cell scenarios (`//go:build integration`): [`openresponses_backend_column_conformance_test.go`](../internal/testkit/conformance/openresponses_backend_column_conformance_test.go)
- Column executable scenario table (`openresponses_backend_column_scenarios.go`) and its table-driven executor (`openresponses_backend_column_scenarios_test.go`, `//go:build integration`)

Operation bridging lives in [`openresponsescompat`](../internal/plugins/backends/openresponsescompat/backend.go):
the backend accepts `openresponses.create` plus the legacy message-authority create
operations produced by the bundled OpenAI frontends (`openai.chat_completions`,
`openai.responses`) and the empty operation of the Anthropic/Gemini frontends. Every
legacy call is normalized through the single explicit legacy→ordered-items projector
before any network work; no pairwise translator exists.

- Scenario IDs are derived from the **column executable scenario table**:
  [`OpenResponsesBackendColumnScenarios`](../internal/testkit/conformance/openresponses_backend_column_scenarios.go)
  (`openresponses-column-<frontend>-<suffix>`): `-json-text`, `-sse-text`,
  `-tools`, `-multimodal`, `-usage-commitment`, `-replay-reject`,
  `-phase-reject`, `-itemref-reject`, `-compaction-reject`, `-extension-reject`,
  plus a dedicated `-continuation`, `-cancellation`, `-failover`, and `-no-retry`
  scenario so evidence never points at a generic `-json-text` or
  `-usage-commitment` proof. The OpenResponses frontend cell's compaction proof
  uses the positive `-compaction` suffix (the generic backend declares the
  compaction capability) and its item-reference proof uses the positive
  `-itemref` suffix (the generic backend declares the exact `item_reference`
  item dialect); the four legacy column cells keep `-compaction-reject` and
  `-itemref-reject`.
  `-json-text` also carries the instructions/roles and
  history evidence.
- **Continuation is positive only for the OpenResponses frontend cell** (the
  proxy-owned continuation surface); every legacy column frontend has no
  client-facing previous-response surface, so continuation is honestly
  classified `out_of_scope` there with an exact rationale and no scenario link.
  The table-driven executor runs continuation for the OpenResponses frontend cell
  (asserting the second `previous_response_id` create re-executes through the
  independent refbackend exactly once) and runs cancellation, pre-output
  failover, and post-visible no-retry for every column cell with exact
  request-count assertions.
- Network evidence uses the independent [`internal/refbackend/openresponses`](../internal/refbackend/openresponses/)
  origin with exact ordered-request assertions; negative cells (replay-dialect,
  conflicting-authority, source-specific-extension, unsupported-content) assert
  **zero** reference-backend requests.

## Authoritative 5×9 = 45-cell matrix (spec Phase 8, Task 8.5)

`BundledFrontendIDs()` = `openai-responses`, `openai-legacy`, `anthropic`, `gemini`,
`openresponses`; `BundledBackendIDs()` = the six essential backends plus
`openresponses`, `openrouter`, `nvidia`. `AllCells()` yields exactly 45 unique
deterministic cells, each classified with a construct/mount driver
([`matrix_test.go`](../internal/testkit/conformance/matrix_test.go) asserts the
count, uniqueness, ordering, metadata, and driver). Every cell classifies the
seventeen required features with release-ready linked evidence
([`matrix_evidence_45.go`](../internal/testkit/conformance/matrix_evidence_45.go),
validated by [`matrix_45_evidence_test.go`](../internal/testkit/conformance/matrix_45_evidence_test.go)).

- All 45 baseline text cells run through the all-cells loops (non-streaming +
  streaming), tools/multimodal run for every viable cell (with pre-network
  rejections asserted for unrepresentable forms), and error shapes map stably.
- OpenRouter/NVIDIA cells are driven through the actual connector executables
  (`connector_host.go` builds and launches `connectors/openrouter` /
  `connectors/nvidia` and drives each backend through the backendplugin host
  adapter APIs; `DeployConnectorColumnFor` / `deployConnectorChain` compose the
  frontend → core → connector backend → observing origin path). The connectors
  advertise streaming-only capabilities, so canonical tools and multimodal input
  are rejected before network on those cells, and the connectors' stream decoder
  surfaces no assistant media output (rejected before network too). Connector-
  specific headers/credentials/inventory/error evidence is executed in
  [`connector_columns_matrix_test.go`](../internal/testkit/conformance/connector_columns_matrix_test.go).
- ACP cells are driven through the actual relocated executable connector: the
  harness builds `connectors/acp` once per test binary and launches a dedicated
  `lip-backend-acp` process per cell, configured with the cell's observing origin
  and driven through the backendplugin host adapter APIs
  ([`connector_host.go`](../internal/testkit/conformance/connector_host.go),
  generalized from the original ACP harness in `acp_connector.go`); ACP stays an
  optional connector column (never an essential backend kind) and its protocol
  adapter is never linked into the root module.
- Full-path compliance: `make test-openresponses-compliance` runs the independent
  client → frontend → core → OpenResponses backend → independent provider path,
  the direct independent-emulator wire suites, the 45-cell matrix, and the
  emulator boundary gates (spec Requirement 12.11/13.18). `make qa` wires the
  fast Task 8.5 compliance gate (`test-openresponses-compliance-static`), which
  verifies the scripts/Makefile/docs wiring and the release-ready evidence
  without re-running the huge tagged suites `qa-tests` already covers; the full
  script remains the standalone Task 8.5 release gate.

### General matrix cells (32) — executable scenario table

The **general matrix cells** are the 45 cells that belong to neither the
OpenResponses frontend row (FE=`openresponses`) nor the OpenResponses backend
column (BE=`openresponses`). `GeneralMatrixCells()` ([`matrix.go`](../internal/testkit/conformance/matrix.go))
computes them as `AllCells()` minus that 13-cell union (9 row + 5 column − 1
shared overlap), so exactly **4×8 = 32 cells**. A review note claimed 24; the
actual count is 32 and is pinned by `TestGeneralMatrix_Exactly32CellsNoOpenResponses`.

Every general cell has an **executable scenario table** entry
([`matrix_general_scenarios.go`](../internal/testkit/conformance/matrix_general_scenarios.go))
for each feature that has an executable proof. The tagged integration test
[`matrix_general_conformance_test.go`](../internal/testkit/conformance/matrix_general_conformance_test.go)
consumes the same table directly and executes each scenario through a real
deployment, so the evidence builder derives every general-cell scenario ID from
the table (`TestMatrix45_EvidenceScenarioIDsComeFromExecutableTable` proves the
evidence scenario-ID set equals the executable table set — no metadata-only IDs;
the table composes the general, row, and column executable registries, so there
is **no row/column exemption** and all 45 evidence IDs must correspond to
executed tables):

| Feature | Executable proof (per general cell) | Outcome |
|---|---|---|
| JSON text / instructions / history | JSON round trip, exact text, exactly one upstream request (ACP ≥ 1) | lossless |
| SSE text | SSE round trip over the same canonical stream | lossless |
| Tools | raw tool create; ACP rejects with zero requests, others project to the wire | projection / reject (ACP, openrouter, nvidia) |
| Multimodal | raw image create with upstream projection; ACP projects to resource blocks | projection / lossless (native) / reject (openrouter, nvidia) |
| Usage/errors | upstream 500 → stable client-visible error envelope, upstream attempt | lossless |
| Cancellation/backpressure | blocking origin + client cancel → upstream stops, candidate untouched | lossless |
| Failover | failing primary + succeeding candidate; **ACP cells fail over too** (the ACP connector classifies transport/HTTP 5xx/protocol failures before canonical output as recoverable pre-output, with terminal auth/validation staying terminal) | lossless |
| No-retry after visible output | mid-stream-death origin emits first content then dies; candidate receives zero requests | lossless |
| Reasoning replay / phase / item refs / compaction / extensions | raw unrepresentable wire form → rejected with zero upstream requests | rejected_before_network |
| Continuation | **out-of-scope** for all general cells (no general frontend exposes proxy-owned continuation; legacy APIs lack a parent concept and the openai-responses frontend does not materialize `previous_response_id`). Proxy-owned continuation is positive in the OpenResponses frontend row (and the OpenResponses frontend column cell) via the executable `-continuation` scenario | out_of_scope |
| Assistant media output | Backends that emit canonical `assistant_image_ref`/`assistant_file_ref` events from their native wire (openai-responses, anthropic, gemini) deliver the media reference to the actual client wire (lossless native same-wire cells, projection cross-protocol); backends without a native assistant-media output surface (openai-legacy chat, bedrock Converse, ACP, and the openrouter/nvidia connectors whose stream decoder surfaces no assistant media output) reject assistant-media requests before any network request with zero upstream requests — the executable `assistant-media` scenario inspects the client wire/media reference and the remote count for every general cell | lossless / projection / reject (openai-legacy, bedrock, acp, openrouter, nvidia) |

Connector-column cells (`*` × openrouter/nvidia) execute through the actual
connector executables (`deployConnectorChain`), including the failover chain.

## Integration-only suites (build tag)

The **default** `go test ./...` / `make test` run **does not compile** `//go:build integration` files. The following live in [`internal/testkit/conformance/`](..) and run when you pass **`-tags=integration`** (CI uses `-tags=precommit,integration`; see [`.github/workflows/qa.yml`](../.github/workflows/qa.yml)):

| Area | Sources |
|------|---------|
| FE×BE matrix structural checks | [`matrix_test.go`](../internal/testkit/conformance/matrix_test.go) |
| Text / tools / multimodal matrix loops | [`conformance_text_test.go`](../internal/testkit/conformance/conformance_text_test.go), [`conformance_tools_test.go`](../internal/testkit/conformance/conformance_tools_test.go), [`conformance_multimodal_test.go`](../internal/testkit/conformance/conformance_multimodal_test.go), [`backend_credentials_test.go`](../internal/testkit/conformance/backend_credentials_test.go) |
| Authenticated streaming parity | [`conformance_stream_authenticated_test.go`](../internal/testkit/conformance/conformance_stream_authenticated_test.go) |
| Protocol parity suites | `parity_*_test.go` (see [`parity_evidence.go`](../internal/testkit/conformance/parity_evidence.go) / [conformance-golden-coverage.md](conformance-golden-coverage.md)) |
| Migration goldens | [`migration_test.go`](../internal/testkit/conformance/migration_test.go) |

Local command mirroring CI conformance compilation:

**Recommended (Makefile target):** `make parity-checks` runs `go test` on [`internal/testkit/conformance/`](../internal/testkit/conformance/) with **`-tags=integration`**, so FE×BE matrix loops, parity suites, migration goldens, and related integration sources compile and run—matching what you get from CI’s full unit pass for that package.

Narrow equivalent:

```bash
go test -tags=integration ./internal/testkit/conformance/...
```

[`conformance_tier_presence_test.go`](../internal/testkit/conformance/conformance_tier_presence_test.go) (no integration tag) only verifies that expected filenames still exist on disk.
