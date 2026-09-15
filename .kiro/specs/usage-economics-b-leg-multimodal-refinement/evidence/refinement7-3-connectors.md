# Refinement 7.3 executable-connector census

Status: `READY_FOR_REVIEW_REFINEMENT_7_3_CONNECTORS`

Base revision: `13621715`

Scope: all current executable modules directly under `connectors/`. This
census records media capability/mapping evidence and economic coverage only;
it does not change root providers, allocation, pricing, or Kiro state.

## Mechanical inventory

The inventory enumerated every `connectors/*/go.mod` and corresponding
`manifest/template.backendplugin.json`, then scanned production Go sources and
connector fixtures for canonical image/file/video/audio/document handling,
media capability bits, and explicit non-text rejection. The complete 34-row
ledger is [`refinement7-3-connectors-census.tsv`](refinement7-3-connectors-census.tsv).

| Result | Count |
| --- | ---: |
| Connector modules scanned | 34 |
| Media-relevant rows (claim, map, reject, or media-shaped output) | 18 |
| `v2-certified` executable producers | 0 |
| `lossless-v1-bridge` producers | 6 |
| `unsupported advanced evidence` rows | 12 |
| Text-only/non-media controls | 16 |

The six V1-bridge rows are `codex`, `commandcode-anthropic`, `cursorsdk`,
`gitlabduo`, `minimexoauth`, and `vertex`. They use the negotiated public
six-counter bridge. None transports a trusted executable `StoreID`/B-leg
subject for native media units, account/resource gauges, provider charge
identity, or advanced revisions, so those fields remain explicitly
unsupported. No connector manifest advertises media or AccountingEvidenceV2;
the media claims found in service/inventory code are therefore checked against
the actual route mapper rather than treated as certification.

## Media classifications

- ACP (`acp`, `agycliacp`, `cursorcliacp`, `geminicliacp`) maps canonical
  image/file refs to shared ACP resource blocks. The prompt fixture proves the
  mapping, while no executable native usage subject or media meter exists.
  These rows are explicitly unsupported for advanced economics.
- Codex, commandcode Anthropic, Cursor SDK, GitLab Duo, MiniMax OAuth, and
  Vertex retain the existing six-counter V1 evidence. Codex and Vertex have
  canonical media request/output mapping; commandcode has an image block
  mapper; Cursor/GitLab/MiniMax are text-only or fail-closed on the media
  product path. Native media units and trusted V2 identity remain unsupported.
- Cohere, OCI, Replicate, SageMaker, and Watsonx explicitly reject non-text
  input or fail closed on media-shaped output. They have no media economic
  producer, so the rejection is the bounded unsupported disposition.
- Ollama previously promoted the provider `/api/show` `vision` hint to a
  capability even though its OpenAI-compatible mapper rejects canonical media.
  The inventory now keeps that hint out of advertised capabilities until a
  lossless mapper exists.
- OpenCode previously advertised Vision/Documents on both factories,
  resolved profiles, and inventory rows even though its Anthropic/Gemini
  routes are text-only and its OpenAI-compatible routes reject canonical media.
  Those overclaims are removed and covered by a descriptor/profile/inventory
  regression fixture. The text-only Anthropic/Gemini request builders now also
  reject canonical image/file/video parts before serialization, including
  item-authority `ToolResultItem.Parts`, so unsupported media cannot be silently
  discarded on a direct route call.
- Qwen's hook preserves raw `image_url` objects, but canonical OpenAI-
  compatible media conversion rejects before the hook. Raw preservation is not
  treated as a certified provider media path.

The 16 controls (`azure`, `cloudflare`, `commandcode-openai`, `databricks`,
`huggingface`, `infomaniak`, `llamacpp`, `lmstudio`, `localstub`, `nousportal`,
`nvidia`, `openrouter`, `sapaicore`, `snowflake`, `vllm`, `xaioauth`) have no
connector-local media mapper or media capability. Their generic compatible
path rejects canonical image/file parts; model-name filters mentioning media
are not capability evidence.

## RED/GREEN fixes

The OpenCode capability regression was first added as a failing fixture:

```text
$env:GOWORK='off'; go test -count=1 -run '^TestMediaCapabilitiesRequireCanonicalMediaMapping$' ./...
FAIL: factory "opencode-go" advertises media without a canonical mapping
```

The Ollama inventory fixture was likewise changed first and failed while
`capsFromOllama` still promoted `vision`:

```text
$env:GOWORK='off'; go test -count=1 -run '^TestParity_LocalInventoryCapsAndError$' ./...
FAIL: ... Capabilities:{... Vision:true ...}
```

The minimal GREEN changes remove OpenCode Vision/Documents from static,
resolved, and inventory capabilities, and stop promoting Ollama's `vision`
provider hint. Both focused fixtures then pass.

## Changed connector files

- `connectors/opencode/internal/service/service.go`
- `connectors/opencode/internal/service/inventory.go`
- `connectors/opencode/internal/upstream/anthropic.go`
- `connectors/opencode/internal/upstream/gemini.go`
- `connectors/opencode/internal/upstream/media_test.go`
- `connectors/opencode/parity_suite_test.go`
- `connectors/ollama/internal/service/caps.go`
- `connectors/ollama/parity_suite_test.go`
- `refinement7-3-connectors-census.tsv`

No manifest schema or export was changed. Existing dirty files from the other
refinement tasks were preserved.

## Economic coverage rule

The ledger makes three independent facts explicit for every row: the media
surface/capability (including a negative capability), the provider economics or
evidence actually emitted (including an explicit absence), and the resulting
action/non-action with its bounded reason. A media reference or a provider
vision hint is not treated as usage or a charge. A row is `v2-certified` only
when its native provider fields and trusted identity are transported; a
`lossless-v1-bridge` row is limited to the negotiated six-counter bridge; and
`unsupported advanced evidence` records a real media surface with no certified
native economics. The 16 `not-media-capable` controls have neither a media
mapper nor a media capability and reject canonical media through their shared
adapter where applicable.

The resulting dispositions are internally consistent: zero executable
connectors claim `v2-certified`, six expose only the V1 bridge, twelve have a
media-shaped or rejecting surface but no advanced economics, and sixteen are
text-only controls. No connector manifest advertises media or
`AccountingEvidenceV2`.

## Verification

The inventory was mechanically checked against all 34 current
`connectors/*/go.mod` modules and matching manifests. Every ledger evidence
path exists, every named test/symbol anchor resolves against the current tree,
and module/disposition counts are 34/18/0/6/12/16 as reported above.

Fresh checks on the current worktree:

```text
GOWORK=off go test -count=1 ./... in each of the 34 connector modules
  PASS (all modules)
GOWORK=off go vet ./... in each of the 34 connector modules
  PASS (all modules)
go test -count=1 ./internal/qa
  PASS
go test -count=1 ./internal/archtest -run 'OpenCode|Phase8_|TestPhase1ProducerConsumerCensusIsExactAndDispositioned|TestKeepwarmFeatureRemainsProviderNeutral|TestUsageAuthoritySourceDoesNotReferenceProviderLocalQuotaMetadata'
  PASS
make parity-checks
  PASS (Windows-selected root/TCK/compatible/ACP/OpenRouter/hosted batches)
make parity-ollama-plugins
  PASS
go test -count=1 -run '^TestTextOnlyRequestBuilders' ./internal/upstream
  PASS (OpenCode media fail-closed RED/GREEN regression)
go test -count=1 ./internal/upstream
  PASS (OpenCode module rerun)
go vet ./... in connectors/ollama and connectors/opencode
  PASS
gofmt -d connectors/ollama/internal/service/caps.go connectors/ollama/parity_suite_test.go connectors/opencode/internal/service/inventory.go connectors/opencode/internal/service/service.go connectors/opencode/internal/upstream/anthropic.go connectors/opencode/internal/upstream/gemini.go connectors/opencode/internal/upstream/media_test.go connectors/opencode/parity_suite_test.go
  PASS (no output)
git diff --check
  PASS
```

`make parity-opencode-plugins` runs the OpenCode module selector successfully
but its Windows recipe then invokes root `go test` without
`./internal/archtest`, producing the pre-existing `no Go files in ...` failure.
The correctly scoped OpenCode/Phase8 archtest command above passes; this
script defect is retained as a residual verification risk and is outside the
connector census ownership. No root census, manifest, pricing, allocation,
or Kiro task status was changed.

The fail-closed OpenCode regression followed strict TDD: the new media cases
first failed because both text-only builders returned nil errors after
discarding image content; the minimal shared `validateTextOnlyCall` check then
made all image, file, and video cases—including structured tool-result parts—
pass while preserving text-only requests.
