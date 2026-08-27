# Implementation Plan

## Execution Rules for All Tasks

These rules are mandatory for every executor working from this plan:

1. **Do not perform broad provider research.** `research.md` is the frozen implementation input. Use its exact profile IDs, family choices, base URLs, env-var names, inventory decisions, connector classifications and source pins.
2. Before adding a provider, search the current implementation branch for equivalent support. If equivalent support has landed since the spec baseline, convert only that provider task into verification/docs/test work; do not duplicate it.
3. **No ACP work is allowed.** Do not modify ACP production connectors/support or add a new ACP provider.
4. A normal compatible provider is a row in `internal/providerprofiles/catalog.json`, not a new Go backend package.
5. Do not expand `lip.provider-profile/v1` to make a difficult provider fit. Dynamic endpoint identity, multi-value auth/addressing, OAuth, cloud signing, control-plane discovery and native non-compatible APIs belong in external connectors/bridges.
6. Never infer native `/responses` or `/messages` support from LiteLLM or another translating intermediary. Follow the frozen protocol decision. Official provider contracts override secondary-source classifications.
7. Responses is preferred wherever the frozen matrix selected it.
8. Provider profiles must be conservative about capabilities. Do not leave unreviewed family-maximum capabilities enabled.
9. Do not assign a tokenizer unless `research.md` or an existing provider-family rule explicitly requires one.
10. Keep default tests offline. Use `httptest`, SDK stubs, public-schema fixtures or connector contract harnesses; live provider credentials may only be optional/env-gated evidence.
11. If one provider's upstream contract now contradicts `research.md`, stop **that provider task only**, record the exact contradiction, and continue independent tasks. Do not invent a new protocol/auth architecture.
12. A task that discovers a need for canonical/core/frontend/backend-plugin ABI changes is blocked and requires separate design review; do not push those changes through this bulk spec.

## Task Graph

```text
1 Profile foundation characterization
  |
  +--> 2 Responses-first profiles
  |      |
  |      +--> 3 Chat profile batches
  |      +--> 4 Anthropic profile batch
  |             |
  |             `--> 5 Profile docs/release contract
  |
  +--> 6 Managed/dynamic compatible connectors (parallel by connector)
  +--> 7 Native cloud/provider connectors (parallel by connector)
  +--> 8 OAuth/subscription bridges (parallel after common auth pattern)
                 |
                 `--> 9 Final provider inventory/docs/release gates
```

Profile batches are sequential/small because they edit the same embedded catalog and expected-ID fixture. Individual external connectors are separate modules and may be implemented in parallel after Task 1.

---

- [ ] 1. Freeze the real provider-profile population contract
- [ ] 1.1 Add an expected embedded-provider characterization table
  - Create `internal/providerprofiles/catalog_population_test.go` (or equivalently focused `_test.go`).
  - Define a test-only table with stable fields: `ID`, `Family`, `BaseURL`, `AuthMode`, `EnvVar`, `Discovery`, optional static model IDs, expected disabled capabilities.
  - Seed it from the full frozen profile matrix in `research.md`; it is never runtime input.
  - Assert exact ID uniqueness, no `example-*` placeholder after real population, no ACP IDs, and no semantic duplicate of an existing dedicated backend/connector.
  - Assert required suffix pairs exist together (`deepseek-responses` + `deepseek-openai`, `scaleway-responses` + `scaleway-openai`).
  - Assert every real profile carries an explicit reviewed capability posture. Long-tail Chat rows must at minimum disable `vision`, `documents`, `reasoning`, `parallel_tool_calls` unless frozen evidence deliberately says otherwise. Anthropic rows disable unproven `reasoning_replay`, media and parallel-tool capability.
  - Observable completion: one mutated endpoint/family/env/capability or deleted expected ID fails the test.
  - _Requirements: 1,2,3,4,8,9,10,12_
  - _Boundary: tests/provider-profile contract_
  - _Depends: none_
  - _Validation: `go test ./internal/providerprofiles/...`_

- [ ] 1.2 Characterize real catalog expansion through the existing binding
  - Extend `internal/standardplugins/provider_profile_binding_test.go` using **real embedded profiles**, not duplicate hand-written profiles, for one Responses, one Chat and one Anthropic row after those rows land.
  - Assert `kind: provider-profile` expands to the intended existing compatible kind, source config is immutable, `backend_prefix == profile.ID`, and base URL/env-var root/inventory/capability ceiling match catalog data.
  - Use `httptest` to prove representative Responses and Anthropic profiles select the correct operation path/auth header without real network calls.
  - Do not add a production factory, registry or provider-specific switch.
  - _Requirements: 1,3,4,7,8,10_
  - _Boundary: tests/config-wiring_
  - _Depends: 1.1 and representative rows from Tasks 2/4 may land in same bounded branch_
  - _Validation: `go test ./internal/standardplugins/... -run 'ProviderProfile|Compatible'`_

- [ ] 1.3 Preserve profile schema and scale guardrails
  - `schema.go`, `compiler.go`, and `provider_profile_binding.go` should remain unchanged unless an existing characterization proves a pre-existing bug independent of population.
  - Keep 1,000-profile bounded/no-goroutine/no-factory scaling proof passing.
  - Keep non-preserve namespace modes, disabled discovery, arbitrary transforms, unsupported quirks, unsafe headers, literal secrets and remote HTTP endpoints fail-closed.
  - Add/retain an assertion that catalog population does not create one runtime factory/contribution per profile.
  - _Requirements: 1,4,8,10_
  - _Boundary: tests/architecture guardrail_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/providerprofiles/...` plus profile-only gate_

---

- [ ] 2. Add the Responses-first and explicit multi-flavor strategic profiles
- [ ] 2.1 Add the exact bare Responses profiles
  - Add `fireworks`, `groq`, `digitalocean`, `vercel-ai-gateway`, `requesty`, and `meta` using `family: openai-responses-compatible` and exact base/env data from `research.md`.
  - Use `family_default` inventory only where frozen matrix permits; otherwise use the frozen static set.
  - Omit tokenizer unless explicitly frozen.
  - Explicitly disable all unproven family-max capabilities. Retain tools/reasoning only where frozen official evidence supports them; do not assume vision/documents/parallel tools.
  - Do not add equivalent Chat/Anthropic aliases just because those APIs also exist.
  - Add every row to the expected-profile test in same batch.
  - **Do not put Kilo here.** Current official Kilo Gateway docs expose OpenAI Chat `/chat/completions`; Kilo is Task 3.2.
  - _Requirements: 1,2,3,4,8,9,10,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/providerprofiles/... && go test ./internal/standardplugins/... -run 'ProviderProfile|Compatible' && make profile-only-check PROFILE_ONLY_BASE=<batch-base-sha> && make parity-checks`_

- [ ] 2.2 Add DeepSeek as an explicit flavor split
  - Add `deepseek-responses`: Responses family, base `https://api.deepseek.com`, env `DEEPSEEK_API_KEY`, **static inventory restricted to `deepseek-v4-flash`**.
  - Add `deepseek-openai`: Chat family, same base/env; use family-default `/models` only if existing discovery fixture conforms, otherwise static Flash+Pro.
  - Do not add bare `deepseek` or redundant Anthropic alias.
  - Retain reasoning where frozen model evidence supports it; disable unproven media/parallel capabilities.
  - Add offline test proving the Responses inventory can never surface the Pro-only model.
  - _Requirements: 2,3,8,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 1.1_
  - _Validation: profile-batch gate from 2.1_

- [ ] 2.3 Add Scaleway as a flavor-correct split
  - Add `scaleway-responses`: Responses family, base `https://api.scaleway.ai/v1`, env `SCW_SECRET_KEY`, static Responses-supported inventory seeded from frozen serverless list including `openai/gpt-oss-120b:fp4` and `openai/gpt-oss-20b:fp4` when present in frozen fixture.
  - Add `scaleway-openai`: Chat family, same base/env, family-default `/models` for broader Chat set.
  - Do not add bare `scaleway`.
  - Add test proving a Chat-only model cannot appear under Responses profile.
  - _Requirements: 2,3,8,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 1.1_
  - _Validation: profile-batch gate from 2.1_

---

- [ ] 3. Populate the OpenAI Chat compatible catalog in bounded batches
- [ ] 3.1 Add Chat profiles A-C
  - Add exactly: `302ai`, `abacus`, `abliteration-ai`, `ai-router`, `aiand`, `aihubmix`, `aki-io`, `alibaba`, `alibaba-cn`, `alibaba-coding-plan`, `alibaba-coding-plan-cn`, `alibaba-token-plan-cn`, `ambient`, `amd`, `anyapi`, `arcee`, `auriko`, `baseten`, `berget`, `blueclaw`, `cerebras`, `chutes`, `clarifai`, `claudinio`, `cline-pass`, `cloudferro-sherlock`, `coralbricks`, `cortecs`, `crof`, `crossmodel`, `crusoe`.
  - Copy exact base/env data from `research.md`; family is `openai-chat-compatible` for every row.
  - Do **not** add Alibaba Token Plan International: existing `alibaba-token-plan-intl` owns that product.
  - Default capability posture: streaming+tools; disable `vision`, `documents`, `reasoning`, `parallel_tool_calls` unless frozen evidence explicitly enriches one row.
  - Use family-default `/models` unless research/task explicitly requires static. Nonconforming model-list fixture -> switch only that row to static; no new parser.
  - Add every row to expected-profile table in same batch.
  - _Requirements: 1,3,4,8,9,10,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 2_
  - _Validation: profile-batch gate from 2.1_

- [ ] 3.2 Add Chat profiles D-M, including Kilo
  - Add exactly: `daoxe`, `deepinfra`, `dinference`, `drun`, `ebcloud`, `echo`, `edenai`, `empiriolabs`, `evroc`, `fastrouter`, `friendli`, `frogbot`, `gmicloud`, `greenpt`, `helicone`, `hetzner`, `hpc-ai`, `hyper`, `iflowcn`, `impossibl`, `inception`, `inceptron`, `inference-net`, `inferx`, `io-net`, `jalapeno`, `jiekou`, `kenari`, `kilo`, `llmgateway`, `llmtech`, `llmtr`, `longcat`, `lucidquery`, `meganova`, `mistral`, `mixlayer`, `moark`, `modal`, `model-oracle-ai`, `modelis`, `modelscope`, `moonshot`, `moonshot-cn`, `morph`.
  - Use exact base/env data from `research.md`.
  - `kilo`: base `https://api.kilo.ai/api/gateway`, env `KILO_API_KEY`, Chat family. This follows Kilo's current official Quickstart/API Reference; do not implement Cline's secondary Responses classification.
  - `inference-net` intentionally differs from surveyed generic `inference` ID to avoid ambiguous generic identity.
  - `morph`: conservative text-only unless a previously frozen fixture proves tools; disable `tools` as well as vision/documents/reasoning/parallel.
  - `mistral`: first run existing compatible-family offline fixture against frozen Mistral Chat/tool shape. If it cannot fit without provider-specific translation, stop only `mistral` and move it to a future family-adapter spec; do not patch generic Chat around it.
  - All other rows use default long-tail capability posture.
  - _Requirements: 1,2,3,4,8,9,10,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 3.1_
  - _Validation: profile-batch gate from 2.1_

- [ ] 3.3 Add Chat profiles N-Z
  - Add exactly: `neuralwatt`, `nova`, `novita-ai`, `ofox`, `opper`, `orcarouter`, `ovhcloud`, `pendra`, `pioneer`, `poe`, `poolside`, `qihang-ai`, `qiniu-ai`, `regolo-ai`, `routing-run`, `scnet-token-plan`, `scx-ai`, `siliconflow`, `siliconflow-cn`, `stackit`, `standardcompute`, `stepfun`, `stepfun-cn`, `stepfun-step-plan`, `stepfun-step-plan-cn`, `submodel`, `synthetic`, `tencent-coding-plan`, `tencent-token-plan`, `tencent-tokenhub`, `tensorx`, `the-grid-ai`, `tinfoil`, `together`, `trustedrouter`, `vultr`, `wafer-ai`, `wandb`, `xai`, `xiaomi`, `xiaomi-token-plan-eu`, `xiaomi-token-plan-cn`, `xiaomi-token-plan-sg`, `xpersona`, `zai`, `zai-cn`, `zai-coding-plan`, `zai-coding-plan-cn`, `zeldoc`, `zenifra`, `zenmux`.
  - Use exact base/env data from `research.md`.
  - `xai` is deliberately Chat: current official xAI OpenAPI was authoritative and contained `/v1/chat/completions` but no `/v1/responses`.
  - `nova` is Amazon Nova direct API, distinct from Bedrock.
  - Region/plan identities remain separate where endpoint/entitlement differs.
  - Apply default long-tail capability posture.
  - _Requirements: 1,2,3,4,8,9,10,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 3.2_
  - _Validation: profile-batch gate from 2.1_

- [ ] 3.4 Remove placeholder and lock complete Chat/Responses ID set
  - Remove `example-openai-responses` after real catalog population.
  - Make expected-ID test compare exact final profile set from Tasks 2-4.
  - Add negative assertion forbidding profile duplication of dedicated products such as `openrouter`, `nvidia`, `huggingface`, `opencode-go`, `opencode-zen`, `openai-codex`, `commandcode-*`, `ollama*`, `lmstudio`, `vllm`, `alibaba-token-plan-intl` unless future ownership intentionally changes.
  - _Requirements: 1,9,10,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 3.3,4_
  - _Validation: `go test ./internal/providerprofiles/... && make profile-only-check PROFILE_ONLY_BASE=<profile-program-base-sha>`_

---

- [ ] 4. Add Anthropic-compatible provider profiles
- [ ] 4.1 Add `kimi-coding`
  - Family `anthropic-compatible`; base `https://api.kimi.com/coding/`; auth `api_key_env`, env `KIMI_API_KEY`.
  - Static inventory exactly `k3`, `k3-256k`, `kimi-for-coding`, `kimi-for-coding-highspeed` unless a frozen provider deprecation is already recorded in implementation branch.
  - Do not add equivalent Kimi OpenAI alias because intended model set is the same.
  - Do not spoof Claude Code/OpenCode/Kimi client identifiers. Preserve truthful Go-LIP identity and Kimi anti-tampering requirement.
  - Disable unproven vision/documents/parallel/reasoning_replay.
  - Add offline `/v1/messages` request/stream characterization using `httptest`.
  - _Requirements: 1,2,3,4,8,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 1_
  - _Validation: profile-batch gate from 2.1_

- [ ] 4.2 Add MiniMax API-key profiles
  - Add `minimax`: Anthropic family, base `https://api.minimax.io/anthropic`, env `MINIMAX_API_KEY`, family-default `/v1/models` inventory.
  - Add `minimax-cn`: base `https://api.minimaxi.com/anthropic`, env **`MINIMAX_CN_API_KEY`**. Use family-default China `/anthropic/v1/models` only if fixture matches existing Anthropic inventory contract; otherwise static.
  - Do not conflate with `minimax-oauth` (Task 8.8).
  - Disable unproven reasoning_replay/vision/documents/parallel tools.
  - _Requirements: 1,3,4,8,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 1_
  - _Validation: profile-batch gate from 2.1_

- [ ] 4.3 Add Thinking Machines/Tinker
  - Add `thinking-machines`: Anthropic family, base `https://tinker.thinkingmachines.dev/services/tinker-prod/anthropic/api`, env `TINKER_API_KEY`, static initial model `thinkingmachines/Inkling`.
  - Explicitly disable unsupported/unproven capabilities including `reasoning_replay`; do not claim prompt-cache behavior because Tinker documents `cache_control` as ignored.
  - Add offline request-path/auth/stream fixture under existing Anthropic compatible TCK.
  - _Requirements: 1,3,4,8,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 1_
  - _Validation: profile-batch gate from 2.1_

---

- [ ] 5. Publish provider-profile operator contract
- [ ] 5.1 Add concise first-class profile configuration example
  - Add `config/examples/provider-profiles-bulk.example.yaml` (or repo-local equivalent) demonstrating at most: one bare Responses provider, one split Responses/Chat provider, one Chat provider, one Anthropic provider.
  - Operators specify runtime `id`, `kind: provider-profile`, `config.profile`; do not duplicate full endpoint/env matrix into YAML examples.
  - Verify check-config/routes/inventory behavior without requiring real credentials for structural validation.
  - _Requirements: 7,11_
  - _Boundary: config/docs_
  - _Depends: 2,3,4_
  - _Validation: `make example-config-check`_

- [ ] 5.2 Document complete first-class profile table
  - Add/update provider-profile operator doc with profile ID, product, protocol family, preferred/supplemental status, base endpoint identity, env var, inventory method, region/plan notes and capability caveats.
  - Mark Responses preferred for split providers.
  - Distinguish standard profiles from private `custom-*-compatible` rows and API-key from OAuth/subscription products.
  - Explicitly say ACP is outside this feature.
  - _Requirements: 7,11,12_
  - _Boundary: docs_
  - _Depends: 3.4_
  - _Validation: `make docs-check knowledge-check`_

---

- [ ] 6. Implement managed/dynamic-address compatible connectors (P)
- [ ] 6.1 Cloudflare AI Gateway connector (P)
  - Create `connectors/cloudflare` using standard external connector layout.
  - Use current account-scoped REST API; Responses preferred. Do not use deprecated universal endpoint.
  - Typed config: `account_id`, API-token reference, optional `gateway_id`; construct `https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1` inside connector.
  - Apply documented gateway ID behavior connector-locally; reuse `connector-support/openaicompat` for Responses-compatible mapping rather than second codec.
  - Inventory/filter only models compatible with declared Responses surface.
  - _Requirements: 5,7,8,9,10,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector module tests + `make backend-plugin-cross-platform-qa` + `make backend-plugin-release-gates-static`_

- [ ] 6.2 Azure OpenAI / Azure AI Foundry connector (P)
  - One connector artifact may expose distinct Azure OpenAI/Foundry kinds only when endpoint/deployment semantics require; avoid duplicate SDK implementations.
  - Responses preferred for deployments/regions where current Azure v1 supports it; Chat supplemental for deployments requiring Chat.
  - Typed config owns resource/endpoint, deployment/model mapping, credential mode and required API-version compatibility.
  - Support API-key and Microsoft Entra credential chain; resolved bearer tokens never appear in YAML/diagnostics.
  - Inventory Azure deployed model/resource identities and map to stable Go-LIP IDs.
  - Hard-negative: Responses-required semantics never silently fall back to Chat.
  - Follow Azure contract linked in `research.md`; no provider survey is expected.
  - _Requirements: 5,7,8,9,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 6.3 Snowflake Cortex connector (P)
  - Typed config: account identifier, optional role, PAT/JWT credential reference; documented browser OAuth may be same connector product.
  - Construct `https://{account}.snowflakecomputing.com/api/v2/cortex/v1` internally.
  - Reuse compatible OpenAI transport where wire-compatible; keep account/role/auth connector-local.
  - Inventory only coding/tool-capable model families frozen by support contract.
  - _Requirements: 5,7,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 6.4 Databricks AI connector (P)
  - Typed config: workspace host, token/OAuth source, optional serving endpoint/gateway selector.
  - Construct current workspace AI Gateway/OpenAI-compatible root internally; no provider-profile env substitution/template feature.
  - Reuse shared compatible transport; connector owns workspace auth and serving-endpoint discovery.
  - _Requirements: 5,7,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 6.5 Infomaniak AI connector (P)
  - Typed config: `product_id` plus API-key reference.
  - Construct `https://api.infomaniak.com/2/ai/{product_id}/openai/v1` and reuse compatible transport.
  - Do not add generic URL-template profile support for this product.
  - _Requirements: 5,7,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

---

- [ ] 7. Implement provider-native and managed-cloud connectors (P)
- [ ] 7.1 Google Vertex AI connector (P)
  - Create `connectors/vertex`, distinct from existing Gemini API-key backend.
  - Typed config: GCP project, location, optional publisher/model/deployment selectors, credential source.
  - Use ADC/service-account/workload identity through provider-supported Google auth/client library inside connector.
  - Prefer native Vertex/Gemini execution contract; do not force all Vertex models through OpenAI compatibility for convenience.
  - Inventory publisher/model catalog scoped by project/location with stable canonical IDs.
  - _Requirements: 5,7,8,9,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 7.2 AWS SageMaker connector (P)
  - Create `connectors/sagemaker` using AWS SDK v2 inside connector module.
  - Typed config: region/profile/credential-chain options, endpoint name and declared inference contract for selected deployment.
  - Use SigV4/default AWS chain and Runtime `InvokeEndpoint`/supported streaming equivalent.
  - Enumerate endpoints through SageMaker control plane but never claim arbitrary containers are OpenAI-compatible; route only configured deterministic contracts.
  - _Requirements: 5,7,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 7.3 OCI Generative AI connector (P)
  - Create `connectors/oci` with typed region, compartment, endpoint/model selectors and OCI credential/signing source.
  - Use OCI signing/workload identity; do not convert credentials into static bearer YAML.
  - Enumerate generative models/endpoints and preserve stable Go-LIP IDs across generated endpoint resources.
  - _Requirements: 5,7,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 7.4 IBM watsonx.ai connector (P)
  - Create `connectors/watsonx` with service/region, project-or-space ID and IBM credential reference.
  - Implement IAM token acquisition/refresh connector-locally and native watsonx chat/text mapping.
  - Enumerate supported foundation/deployed language models and filter to declared Go-LIP semantics.
  - _Requirements: 5,7,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 7.5 SAP AI Core connector (P)
  - Create `connectors/sapaicore`.
  - Accept service-key **reference**, parse `clientid`, `clientsecret`, auth URL and `serviceurls.AI_API_URL` connector-locally; never project secret-bearing service key to diagnostics.
  - Acquire OAuth client-credentials token and apply configured `AI-Resource-Group` where required.
  - Discover deployments and map deployment URL/model to stable canonical ID.
  - Reuse compatible transport after deployment resolution only when deployment is actually compatible.
  - _Requirements: 4,5,7,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 7.6 Cohere native connector (P)
  - Create `connectors/cohere` against native `POST /v2/chat` rooted at `https://api.cohere.com`.
  - Map canonical messages/tools/stream directly; no LiteLLM translation and no pretending native API is OpenAI Chat.
  - Enumerate language models through Cohere model API and expose only supported coding/text capabilities.
  - Hard-negative tests for canonical semantics Cohere cannot preserve.
  - _Requirements: 5,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 7.7 Replicate language-model connector (P)
  - Create `connectors/replicate`, root `https://api.replicate.com/v1`, bearer `REPLICATE_API_TOKEN` or connector-standard secret reference.
  - Own prediction create, stream/poll, terminal result, cancellation and cleanup; asynchronous lifecycle must not be hidden in generic retry logic.
  - Enumerate models/versions but expose only language-model deployments with configured/frozen canonical mapping. Arbitrary Replicate schemas are out of scope.
  - Cancellation stops local I/O and provider prediction when cancellable.
  - _Requirements: 5,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

---

- [ ] 8. Implement non-ACP OAuth/subscription bridges (P)
- [ ] 8.1 Establish connector-local OAuth credential pattern without universal auth framework
  - Reuse secure credential/account-store patterns from existing Codex connector where applicable: restrictive files, atomic updates, redacted diagnostics, refresh-before-expiry, terminal refresh quarantine, explicit re-login.
  - Do not add OAuth concepts to `pkg/lipapi` or core routing.
  - Shared helper under `connector-support` only after at least two bridges need exact same PKCE/device primitive; provider endpoints/scopes/entitlements remain connector-local.
  - Tests cover state/PKCE validation, refresh, `invalid_grant`/4xx quarantine, logout/removal, no repeated terminal refresh replay.
  - _Requirements: 4,6,8,10_
  - _Boundary: connector-support/external plugins_
  - _Depends: 1_
  - _Validation: connector-support tests + connector release gates_

- [ ] 8.2 GitHub Copilot direct HTTP subscription bridge (P)
  - **Do not use Copilot ACP.** Direct service access only.
  - Use current supported GitHub device/OAuth credential route and Copilot service-token/model entitlement flow from pinned surveyed implementations; service identity is `https://api.githubcopilot.com`.
  - Inventory from Copilot model entitlement service, not all GitHub Models.
  - Reference Hermes Agent commit `6dcebea7fc5d0cc4f621eeaddf52b7d877a5f882` auth/provider sources plus current Cline `github-copilot` provider declaration.
  - If token exchange is no longer documented/permitted for third-party products, mark **unsupported-by-policy** and stop. Do not reverse engineer private endpoints or use ACP workaround.
  - _Requirements: 6,8,11,12_
  - _Boundary: external backend plugin/auth bridge_
  - _Depends: 8.1_
  - _Validation: offline OAuth/token-exchange fixtures + connector tests_

- [ ] 8.3 GitLab Duo / Duo Agent Platform bridge (P)
  - **No ACP.** Support GitLab.com and configured self-managed instance.
  - Auth: browser OAuth recommended, PAT supported; self-managed OAuth client ID explicit when required.
  - Support configured AI Gateway/workflow service; dynamically discover `duo-workflow-*` models from namespace/instance and cache only within connector generation lifecycle.
  - Repository-management tools are outside inference connector.
  - Use pinned OpenCode provider docs/implementation at `c77100a40c16a1c7c39115023ccd6f284b476c77`.
  - _Requirements: 6,7,8,11_
  - _Boundary: external backend plugin/auth bridge_
  - _Depends: 8.1_
  - _Validation: OAuth/PAT fixtures + dynamic model discovery + connector tests_

- [ ] 8.4 Claude subscription/OAuth bridge (P)
  - Keep existing `anthropic` API-key backend unchanged; this is distinct credential/billing product.
  - Implement only Anthropic's currently documented/permitted third-party OAuth/setup-token route from pinned Hermes behavior.
  - Do not impersonate Claude Code or claim subscription allowance that route does not actually consume.
  - Provider policy prohibiting third-party route -> record unsupported and stop, no bypass.
  - Inventory only entitled models.
  - _Requirements: 4,6,8,11_
  - _Boundary: external backend plugin/auth bridge_
  - _Depends: 8.1_
  - _Validation: auth-store/refresh fixtures + connector tests_

- [ ] 8.5 Nous Portal connector (P)
  - Distinct subscription gateway; do not conflate with direct Nous API-key inference.
  - Follow pinned Hermes `6dcebea7fc5d0cc4f621eeaddf52b7d877a5f882`: OAuth-managed credentials, scoped `inference:invoke` JWT preferred, legacy opaque session-key only if current public contract still requires, rotation/refresh, revoked-token quarantine.
  - Send truthful Go-LIP/AIProxer client identity, never claim Hermes identity.
  - Dynamically inventory Portal provider-qualified model catalog.
  - _Requirements: 4,6,7,8,11_
  - _Boundary: external backend plugin/auth bridge_
  - _Depends: 8.1_
  - _Validation: scoped-token/refresh/catalog fixtures + connector tests_

- [ ] 8.6 xAI subscription OAuth bridge (P)
  - Keep API-key `xai` Chat profile separate.
  - Implement only current documented xAI subscription/device/browser authorization; resulting bearer uses same supported xAI model-service semantics.
  - OAuth does not automatically change wire family to Responses.
  - Entitlement 403 after successful auth is entitlement failure, not endless refresh.
  - Use pinned Hermes xAI OAuth tests/docs at `6dcebea7fc5d0cc4f621eeaddf52b7d877a5f882` as behavioral fixtures.
  - _Requirements: 4,6,8,11_
  - _Boundary: external backend plugin/auth bridge_
  - _Depends: 8.1_
  - _Validation: device/OAuth fixtures + entitlement/error tests + connector tests_

- [ ] 8.7 Qwen Portal OAuth bridge (P)
  - Identity `qwen-oauth`; distinct from `alibaba`/DashScope API-key profiles.
  - Inference base `https://portal.qwen.ai/v1` from pinned Hermes provider profile.
  - Port connector-local request adaptations from Hermes `plugins/model-providers/qwen-oauth/__init__.py`: normalize string content to typed text parts, preserve image URL objects, system-last-part ephemeral cache marker only if current Portal accepts it, `vl_high_resolution_images: true`, Qwen session metadata at correct top-level location.
  - Auth is browser/PKCE external OAuth; no fake static API-key requirement.
  - If adaptation needs a new canonical field, block this provider and escalate instead of changing `pkg/lipapi` here.
  - _Requirements: 4,6,8,11_
  - _Boundary: external backend plugin/auth bridge_
  - _Depends: 8.1_
  - _Validation: PKCE/auth fixtures + request-shape golden tests + connector tests_

- [ ] 8.8 MiniMax OAuth bridge (P)
  - Identity `minimax-oauth`; distinct from API-key profiles.
  - Inference base `https://api.minimax.io/anthropic`, Anthropic Messages transport.
  - Port exact pinned Hermes flow from `website/docs/guides/minimax-oauth.md` at `6dcebea7fc5d0cc4f621eeaddf52b7d877a5f882`: PKCE verifier/challenge+state; POST `{base_url}/oauth/code`; open/display verification URI/user code; poll `{base_url}/oauth/token`; persist access+refresh+expiry; refresh within 60s; terminal 4xx/`invalid_grant`/revoked/`refresh_token_reused` -> quarantine; successful re-login clears quarantine.
  - Initial model set `MiniMax-M2.7`, `MiniMax-M2.7-highspeed` unless authenticated enumeration provides authoritative superset.
  - Do not use `MINIMAX_API_KEY` for this product.
  - _Requirements: 4,6,8,11_
  - _Boundary: external backend plugin/auth bridge_
  - _Depends: 8.1_
  - _Validation: exact OAuth state-machine fixtures + Anthropic transport tests + connector tests_

---

- [ ] 9. Finalize complete provider coverage and release evidence
- [ ] 9.1 Reconcile expected provider inventory against repository support
  - Enumerate current built-in backend kinds, embedded provider profiles and source/discovered connector manifest factory kinds against this spec's expected list.
  - Fail on duplicate semantic provider ownership, missing expected profile/connector, unexpected ACP addition or stale placeholder.
  - Optional connector artifacts need not be installed merely to validate source-tree expected set; use manifest/source metadata.
  - _Requirements: 9,10,12_
  - _Boundary: tests/release evidence_
  - _Depends: 3.4,5,6,7,8_
  - _Validation: expected-inventory test + `make backend-plugin-release-gates-static`_

- [ ] 9.2 Update top-level supported-backend documentation
  - Update README/architecture/provider docs so they no longer under-report already-landed support (including Alibaba Token Plan International) and include new profile/connector coverage.
  - Use compact categories plus detailed provider doc rather than 100-row README.
  - For each connector/bridge document: backend kind, auth method, endpoint construction, model enumeration, required config/env, protocol surface and caveats.
  - Separate API-key and OAuth/subscription products visibly; state ACP exclusion.
  - _Requirements: 7,11,12_
  - _Boundary: docs_
  - _Depends: 9.1_
  - _Validation: `make docs-check knowledge-check`_

- [ ] 9.3 Run final architecture and quality gates
  - Run full quality/tests/parity/example/profile-scale/connector static release gates.
  - Confirm ordinary profile population did not edit canonical/core/frontend/ABI/shared-composition production surfaces.
  - Confirm no runtime network fetch of Models.dev/Cline/OpenCode/Hermes/LiteLLM was introduced.
  - Confirm no production ACP changes were made.
  - Confirm external connectors remain optional/closed-manifest and provider SDK dependencies do not leak into root/core packages.
  - _Requirements: 1,4,8,9,10,12_
  - _Boundary: repository-wide validation_
  - _Depends: 9.2_
  - _Validation: `make quality-checks && make test && make qa && make example-config-check && make backend-plugin-release-gates-static && make docs-check knowledge-check`_

## Parallelization Summary

- Tasks 1-5 are profile-catalog work and should be sequential/small rebased batches because they share catalog/expected-table files.
- Tasks 6.1-6.5 may run in parallel after Task 1.
- Tasks 7.1-7.7 may run in parallel after Task 1.
- Task 8.1 establishes shared credential primitives only where justified; Tasks 8.2-8.8 may run in parallel after 8.1.
- Task 9 is the convergence gate after release-intended providers have landed or been explicitly recorded unsupported/deferred under stop rules.

## Implementation Stop Conditions

Stop only the affected provider task and report exact reason when:

- official provider API no longer matches frozen base/protocol/auth contract;
- provider terms explicitly prohibit required third-party subscription/OAuth use;
- implementation requires a new canonical semantic/capability or backend-plugin ABI field;
- a profile needs a new transform/URL-template/multi-secret schema feature rather than current v1 data;
- claimed inventory cannot be made flavor-correct through current remote/static mechanisms;
- native provider would require semantic approximation/loss instead of lossless mapping or explicit rejection.

These are fail-closed conditions, not invitations for a smaller executor to resume broad research or invent architecture.
