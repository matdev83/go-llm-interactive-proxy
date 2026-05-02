# FE×BE conformance matrix — test evidence

The bundled matrix is the Cartesian product of [`BundledFrontendIDs()` and `BundledBackendIDs()`](../internal/testkit/conformance/matrix.go) (24 cells). Subset rules (ACP tools/multimodal) live in [`newCell`](../internal/testkit/conformance/matrix.go).

## How cells are covered (iteration model)

Instead of one test function per cell, the conformance package **iterates `AllCells()`** and runs the same scenario per `openai-responses__anthropic`-style subtest.

| Subset | Matrix filter | Primary tests (all use `for _, cell := range AllCells()` + `t.Run(frontend__backend)`) |
|--------|----------------|----------------------------------------------------------------------------------------|
| **Text** | `TextViable` (always true today) | `TestConformance_TextOnly_roundTrip`, `TestConformance_TextOnly_streamAndNonStreamParity`, `TestConformance_TextOnly_upstreamErrorShape` in [`conformance_text_test.go`](../internal/testkit/conformance/conformance_text_test.go) |
| **Text (credential pool)** | `TextViable` | `TestConformance_CredentialPool_TextOnly_*` in [`backend_credentials_test.go`](../internal/testkit/conformance/backend_credentials_test.go) |
| **Tools** | `ToolsViable` | `TestConformance_Tools_roundTripAndUsage` in [`conformance_tools_test.go`](../internal/testkit/conformance/conformance_tools_test.go) — **excludes** FE×`acp` because [`SubsetMeta`](../internal/testkit/conformance/matrix.go) sets `ToolsViable: false` for ACP |
| **Multimodal** | `MultimodalViable` | `TestConformance_Multimodal_imageInUpstream`, `TestConformance_Multimodal_pdfInUpstream` in [`conformance_multimodal_test.go`](../internal/testkit/conformance/conformance_multimodal_test.go) — **excludes** FE×`acp` |
| **Multimodal (credential pool)** | `MultimodalViable` | `TestConformance_CredentialPool_Multimodal_*` in [`backend_credentials_test.go`](../internal/testkit/conformance/backend_credentials_test.go) |

## Explicit exceptions (no extra tests required beyond matrix meta)

| Cells | Restriction | Reference |
|-------|-------------|-----------|
| All frontends × **`acp`** | Tools and multimodal disabled by design; text-only cells exercised | [`matrix.go` `SubsetJustification`](../internal/testkit/conformance/matrix.go) |

Parity suite files per protocol id remain as in [`conformance-golden-coverage.md`](conformance-golden-coverage.md).

## Drift guard

[`matrix_evidence_test.go`](../internal/testkit/conformance/matrix_evidence_test.go) asserts conformance sources still use the `AllCells()` iteration pattern for text/tools/multimodal tiers.

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
