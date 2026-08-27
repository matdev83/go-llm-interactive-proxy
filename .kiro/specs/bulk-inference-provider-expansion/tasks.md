# Implementation Plan

## Execution Rules for All Tasks

These rules are mandatory for every executor working from this plan:

1. **Do not perform broad provider research.** `research.md` is the frozen implementation input. Use the exact profile IDs, family choices, base URLs, env-var names, inventory decisions, and connector classifications recorded there.
2. Before adding a provider, search the current implementation branch for equivalent support. If equivalent support has landed since the spec baseline, convert only that provider task into verification/docs/test work; do not duplicate it.
3. **No ACP work is allowed.** Do not modify `connectors/acp`, `connector-support/acp`, `agycliacp`, `cursorcliacp`, `geminicliacp`, or add a new ACP provider.
4. A normal compatible provider is a row in `internal/providerprofiles/catalog.json`, not a new Go backend package.
5. Do not expand `lip.provider-profile/v1` to make a difficult provider fit. Dynamic endpoint identity, multi-value auth/addressing, OAuth, cloud signing, control-plane discovery, and native non-compatible APIs belong in external connectors/bridges.
6. Never infer native `/responses` or `/messages` support from LiteLLM's normalized endpoint table. Follow the frozen protocol decision.
7. Responses is preferred wherever the frozen matrix selected it.
8. Provider profiles must be conservative about capabilities. Do not leave unreviewed family-maximum capabilities enabled.
9. Do not assign a tokenizer to a provider unless `research.md` or an existing provider-family rule explicitly requires one.
10. Keep default tests offline. Use `httptest`, SDK stubs, recorded public-schema fixtures, or connector contract test harnesses; live provider credentials may only be optional/env-gated evidence.
11. If one provider's upstream contract now contradicts `research.md`, stop **that provider task only**, record the exact contradiction in the implementation PR/commit notes, and continue independent tasks. Do not invent a new protocol/auth architecture.
12. A task that discovers a need for canonical/core/frontend/backend-plugin ABI changes is blocked and requires a separate design/spec review; do not push those changes through this bulk spec.

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
  +--> 8 OAuth/subscription bridges (parallel by connector)
                 |
                 `--> 9 Final provider inventory/docs/release gates
```

Profile batches are intentionally sequential because they edit the same embedded catalog and expected-ID fixture. Individual external connectors are separate modules and may be implemented in parallel after Task 1 establishes the provider-boundary characterization.

---

- [ ] 1. Freeze the real provider-profile population contract
- [ ] 1.1 Add an expected embedded-provider characterization table
  - Create `internal/providerprofiles/catalog_population_test.go` (or an equivalently focused `_test.go`).
  - Define a test-only table with stable fields: `ID`, `Family`, `BaseURL`, `AuthMode`, `EnvVar`, `Discovery`, optional static model IDs, and expected disabled capabilities.
  - Seed it with the full frozen profile matrix from `research.md`; do not use it at runtime.
  - Assert exact ID uniqueness; no `example-*` placeholder; no ACP IDs; no duplicate semantic product where an existing dedicated Go-LIP backend/connector already owns the product.
  - Assert `deepseek-responses`+`deepseek-openai` and `scaleway-responses`+`scaleway-openai` exist as complete suffix pairs once their batch lands.
  - Assert every real profile carries an explicit reviewed capability posture: long-tail Chat profiles must at minimum disable `vision`, `documents`, `reasoning`, and `parallel_tool_calls` unless the frozen high-value matrix explicitly authorizes a richer set.
  - For Anthropic profiles, assert unproven `reasoning_replay`, `vision`, `documents`, and `parallel_tool_calls` are disabled.
  - Observable completion: the table can fail on one deliberately mutated endpoint/family/env/capability value and on removal/renaming of one expected ID.
  - _Requirements: 1,2,3,4,8,9,10,12_
  - _Boundary: tests/provider-profile data contract_
  - _Depends: none_
  - _Validation: `go test ./internal/providerprofiles/...`_

- [ ] 1.2 Characterize real catalog expansion through the existing binding
  - Extend `internal/standardplugins/provider_profile_binding_test.go` using catalog profiles, not hand-written duplicate test profiles, for one Responses, one Chat, and one Anthropic profile after those rows are available.
  - For each representative row, assert `ExpandProviderProfileRows` maps `kind: provider-profile` to the existing family factory, leaves the source config immutable, derives `backend_prefix == profile.ID`, and carries the frozen base URL/env-var root/inventory policy.
  - Use `httptest` to prove one Responses and one Anthropic real-profile execution maps the expected request path and credential header without a provider network call.
  - Do not add a production factory, registry, or profile-specific switch.
  - Observable completion: changing a real catalog family or base URL breaks this test before runtime integration.
  - _Requirements: 1,3,4,7,8,10_
  - _Boundary: tests/config-wiring_
  - _Depends: 1.1 and first representative rows from Tasks 2/4 may be completed together in one bounded implementation branch_
  - _Validation: `go test ./internal/standardplugins/... -run 'ProviderProfile|Compatible'`_

- [ ] 1.3 Preserve profile schema and scale guardrails
  - Do not modify `schema.go`, `compiler.go`, or `provider_profile_binding.go` unless an existing test proves a pre-existing bug independent of provider population.
  - Keep the 1,000-profile no-goroutine/no-factory scale test passing.
  - Keep `DiscoveryDisabled`, non-preserve namespace modes, arbitrary transforms, unsupported quirks, unsafe headers, literal secrets, and remote HTTP endpoints fail-closed.
  - Add a targeted regression assertion that catalog population does not create one runtime factory/contribution per profile.
  - _Requirements: 1,4,8,10_
  - _Boundary: tests/architecture guardrail_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/providerprofiles/... && make profile-only-check PROFILE_ONLY_BASE=HEAD~1` (use the actual batch base SHA in implementation)_

---

- [ ] 2. Add the Responses-first and explicit multi-flavor strategic profiles
- [ ] 2.1 Add the exact Responses-first profile rows
  - Add these profile identities from the locked table in `research.md`: `fireworks`, `groq`, `digitalocean`, `vercel-ai-gateway`, `requesty`, `kilo`, and `meta`.
  - Use `family: openai-responses-compatible`, the exact frozen base URL and bearer env var, `family_default` inventory only where the matrix permits it, and `namespace.mode: preserve`.
  - Omit tokenizer unless explicitly frozen.
  - Set an explicit capability `disable` list for all unproven family-max capabilities. Retain `tools` and `reasoning` only where the frozen official evidence supports them; disable `vision`, `documents`, and `parallel_tool_calls` unless specifically certified by a focused fixture.
  - Do not create equivalent Chat or Anthropic duplicates merely because the provider also exposes those APIs.
  - Add all rows to the expected-profile test in the same change.
  - _Requirements: 1,2,3,4,8,9,10,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/providerprofiles/... && go test ./internal/standardplugins/... -run 'ProviderProfile|Compatible' && make profile-only-check PROFILE_ONLY_BASE=<batch-base-sha> && make parity-checks`_

- [ ] 2.2 Add DeepSeek as an explicit flavor split
  - Add `deepseek-responses` with family `openai-responses-compatible`, base `https://api.deepseek.com`, env `DEEPSEEK_API_KEY`, and **static inventory restricted to `deepseek-v4-flash`** according to the frozen official Responses model matrix.
  - Add `deepseek-openai` with family `openai-chat-compatible`, the same base/env, and broad Chat inventory (`family_default` only if the existing OpenAI model-list contract fixture succeeds; otherwise freeze Flash+Pro statically).
  - Do not add a bare `deepseek` alias and do not add a third Anthropic identity because it does not unlock a required distinct model population in this spec.
  - Retain reasoning capability where the selected DeepSeek models support it; disable unproven vision/documents/parallel-tool capabilities.
  - Add an offline test that the Responses inventory can never surface the Pro-only model.
  - _Requirements: 2,3,8,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 1.1_
  - _Validation: same profile-batch gate as 2.1_

- [ ] 2.3 Add Scaleway as a flavor-correct split
  - Add `scaleway-responses`, family `openai-responses-compatible`, base `https://api.scaleway.ai/v1`, env `SCW_SECRET_KEY`, with a static Responses-supported inventory seeded from the frozen serverless list (including `openai/gpt-oss-120b:fp4` and `openai/gpt-oss-20b:fp4` when still present in the frozen fixture).
  - Add `scaleway-openai`, family `openai-chat-compatible`, same base/env, with family-default `/models` discovery for the broader Chat set.
  - Do not add bare `scaleway`.
  - Add a test proving a Chat-only model cannot appear under `scaleway-responses`.
  - _Requirements: 2,3,8,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 1.1_
  - _Validation: same profile-batch gate as 2.1_

---

- [ ] 3. Populate the OpenAI Chat compatible catalog in bounded batches
- [ ] 3.1 Add Chat profiles A-C
  - Add exactly: `302ai`, `abacus`, `abliteration-ai`, `ai-router`, `aiand`, `aihubmix`, `aki-io`, `alibaba`, `alibaba-cn`, `alibaba-coding-plan`, `alibaba-coding-plan-cn`, `alibaba-token-plan-cn`, `ambient`, `amd`, `anyapi`, `arcee`, `auriko`, `baseten`, `berget`, `blueclaw`, `cerebras`, `chutes`, `clarifai`, `claudinio`, `cline-pass`, `cloudferro-sherlock`, `coralbricks`, `cortecs`, `crof`, `crossmodel`, `crusoe`.
  - Copy exact base URL/env var from `research.md`; do not normalize brands into different IDs.
  - Do **not** add Alibaba Token Plan International: existing `alibaba-token-plan-intl` owns that product.
  - Family is `openai-chat-compatible` for every row in this subtask.
  - Default capability posture: streaming+tools only; disable `vision`, `documents`, `reasoning`, `parallel_tool_calls`. Only lift `reasoning` for a row if the locked high-value matrix explicitly says so; this batch otherwise stays conservative.
  - Use family-default `/models` unless `research.md` specifically requires static inventory. If an offline captured fixture shows a nonconforming model-list response, switch only that row to a static inventory rather than adding a parser.
  - Add every row to the expected-profile test in the same commit/batch.
  - _Requirements: 1,3,4,8,9,10,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 2_
  - _Validation: profile-batch gate from 2.1_

- [ ] 3.2 Add Chat profiles D-M
  - Add exactly: `daoxe`, `deepinfra`, `dinference`, `drun`, `ebcloud`, `echo`, `edenai`, `empiriolabs`, `evroc`, `fastrouter`, `friendli`, `frogbot`, `gmicloud`, `greenpt`, `helicone`, `hetzner`, `hpc-ai`, `hyper`, `iflowcn`, `impossibl`, `inception`, `inceptron`, `inference-net`, `inferx`, `io-net`, `jalapeno`, `jiekou`, `kenari`, `llmgateway`, `llmtech`, `llmtr`, `longcat`, `lucidquery`, `meganova`, `mistral`, `mixlayer`, `moark`, `modal`, `model-oracle-ai`, `modelis`, `modelscope`, `moonshot`, `moonshot-cn`, `morph`.
  - Use exact base/env data from `research.md`.
  - `inference-net` intentionally differs from the surveyed generic `inference` ID to avoid an ambiguous generic backend identity.
  - `morph` is conservative/text-only unless an existing frozen fixture in the implementation branch proves tool support: disable `tools`, `vision`, `documents`, `reasoning`, and `parallel_tool_calls` for it.
  - For `mistral`, first run the existing compatible-family offline fixture against the frozen Mistral Chat response/tool shape. If it does not fit without provider-specific wire translation, stop `mistral` profile addition and move only that product to a future family-adapter spec; do not patch generic Chat behavior around Mistral.
  - All other rows use the default long-tail capability posture from 3.1.
  - _Requirements: 1,3,4,8,9,10,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 3.1_
  - _Validation: profile-batch gate from 2.1_

- [ ] 3.3 Add Chat profiles N-Z
  - Add exactly: `neuralwatt`, `nova`, `novita-ai`, `ofox`, `opper`, `orcarouter`, `ovhcloud`, `pendra`, `pioneer`, `poe`, `poolside`, `qihang-ai`, `qiniu-ai`, `regolo-ai`, `routing-run`, `scnet-token-plan`, `scx-ai`, `siliconflow`, `siliconflow-cn`, `stackit`, `standardcompute`, `stepfun`, `stepfun-cn`, `stepfun-step-plan`, `stepfun-step-plan-cn`, `submodel`, `synthetic`, `tencent-coding-plan`, `tencent-token-plan`, `tencent-tokenhub`, `tensorx`, `the-grid-ai`, `tinfoil`, `together`, `trustedrouter`, `vultr`, `wafer-ai`, `wandb`, `xai`, `xiaomi`, `xiaomi-token-plan-eu`, `xiaomi-token-plan-cn`, `xiaomi-token-plan-sg`, `xpersona`, `zai`, `zai-cn`, `zai-coding-plan`, `zai-coding-plan-cn`, `zeldoc`, `zenifra`, `zenmux`.
  - Use exact base/env data from `research.md`.
  - `xai` is deliberately **Chat**, not Responses: current official xAI OpenAPI was the authority for this spec and contained `/v1/chat/completions` but no `/v1/responses`.
  - `nova` is Amazon Nova direct API and is distinct from existing AWS Bedrock.
  - Plan/region identities intentionally remain separate where the endpoint/entitlement differs.
  - Apply the same conservative long-tail capability posture as 3.1.
  - _Requirements: 1,2,3,4,8,9,10,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 3.2_
  - _Validation: profile-batch gate from 2.1_

- [ ] 3.4 Remove the placeholder provider and lock the complete Chat/Responses ID set
  - Remove `example-openai-responses` from `catalog.json` after the real catalog is populated.
  - Make expected-ID tests compare the exact final profile set contributed by Tasks 2-4, not a subset-only assertion.
  - Add a negative test forbidding IDs/families for existing dedicated products (`openrouter`, `nvidia`, `huggingface`, `opencode-go`, `opencode-zen`, `openai-codex`, `commandcode-*`, `ollama*`, `lmstudio`, `vllm`, `alibaba-token-plan-intl`) unless future code has intentionally changed ownership.
  - _Requirements: 1,9,10,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 3.3,4_
  - _Validation: `go test ./internal/providerprofiles/... && make profile-only-check PROFILE_ONLY_BASE=<profile-program-base-sha>`_

---

- [ ] 4. Add the Anthropic-compatible provider profiles
- [ ] 4.1 Add `kimi-coding`
  - Family `anthropic-compatible`; base `https://api.kimi.com/coding/`; auth `api_key_env`, env `KIMI_API_KEY`.
  - Static model inventory exactly: `k3`, `k3-256k`, `kimi-for-coding`, `kimi-for-coding-highspeed`, unless the frozen implementation fixture intentionally removes a provider-deprecated ID.
  - Do not add an equivalent Kimi OpenAI profile because it exposes the same intended model population.
  - Do not inject/spoof Claude Code/OpenCode/Kimi client identifiers. Preserve normal Go-LIP identity behavior and Kimi's documented anti-tampering requirement.
  - Disable `vision`, `documents`, `parallel_tool_calls`, and `reasoning_replay` unless a focused frozen contract explicitly proves the exact Go-LIP semantic.
  - Add offline `/v1/messages` request/stream characterization using `httptest`.
  - _Requirements: 1,2,3,4,8,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 1_
  - _Validation: profile-batch gate from 2.1_

- [ ] 4.2 Add MiniMax API-key profiles
  - Add `minimax`: family `anthropic-compatible`, base `https://api.minimax.io/anthropic`, auth env `MINIMAX_API_KEY`, family-default `/v1/models` inventory.
  - Add `minimax-cn`: base `https://api.minimaxi.com/anthropic`; use the API-key env naming frozen in the current implementation matrix. If the China `/anthropic/v1/models` fixture does not match the existing Anthropic inventory contract, use a static inventory rather than a new parser.
  - Do not conflate these with `minimax-oauth`; OAuth is Task 8.7.
  - Disable unproven `reasoning_replay`, vision/documents/parallel tools.
  - _Requirements: 1,3,4,8,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 1_
  - _Validation: profile-batch gate from 2.1_

- [ ] 4.3 Add Thinking Machines/Tinker and conditional `freemodel`
  - Add `thinking-machines`: family `anthropic-compatible`, base `https://tinker.thinkingmachines.dev/services/tinker-prod/anthropic/api`, env `TINKER_API_KEY`, static initial model `thinkingmachines/Inkling`.
  - Explicitly disable unsupported/unproven profile capabilities, including `reasoning_replay`, and do not claim prompt-cache behavior: Tinker documents that `cache_control` is ignored.
  - Add `freemodel` only if the frozen endpoint fixture fits the existing Anthropic family with no new quirk; otherwise record it as deferred and do not change the family adapter.
  - _Requirements: 1,3,4,8,12_
  - _Boundary: provider-profile data/tests_
  - _Depends: 1_
  - _Validation: profile-batch gate from 2.1_

---

- [ ] 5. Publish the provider-profile operator contract
- [ ] 5.1 Add a concise first-class provider configuration example
  - Add one representative `config/examples/provider-profiles-bulk.example.yaml` (or equivalent local naming convention) containing no more than a small set demonstrating: one bare Responses provider, one split Responses/Chat provider, one Chat provider, one Anthropic provider.
  - Operators should only specify runtime `id`, `kind: provider-profile`, and `config.profile`; do not copy the full endpoint/env matrix into YAML examples.
  - Verify with `lipstd check-config`, `routes`, `inventory` using no real credentials where validation permits; network-dependent inventory must not be required for check-config.
  - _Requirements: 7,11_
  - _Boundary: config/docs_
  - _Depends: 2,3,4_
  - _Validation: `make example-config-check`_

- [ ] 5.2 Document the complete first-class profile table
  - Add/update a provider-profile operator document with columns: profile ID, display/provider product, protocol family, preferred/supplemental status, base endpoint identity, env var, inventory method, region/plan notes, and capability caveats.
  - Mark Responses preferred for split providers.
  - Distinguish standard profiles from private `custom-*-compatible` rows.
  - Explicitly say ACP integrations are outside this feature.
  - Do not copy secret values or unstable pricing/model marketing prose.
  - _Requirements: 7,11,12_
  - _Boundary: docs_
  - _Depends: 3.4_
  - _Validation: `make docs-check knowledge-check`_

---

- [ ] 6. Implement managed/dynamic-address compatible connectors (P)
- [ ] 6.1 Cloudflare AI Gateway connector (P)
  - Create `connectors/cloudflare` following the standard external connector packaging layout.
  - Product is Cloudflare's current account-scoped REST API; use Responses as preferred execution surface. Do **not** use the deprecated universal endpoint.
  - Typed config: `account_id`, token env/reference, optional `gateway_id`; construct the endpoint under `https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1` inside the connector.
  - If `gateway_id` is configured, apply the documented gateway header/route behavior; do not encode it as a generic profile static header.
  - Reuse `connector-support/openaicompat` for OpenAI-compatible request/stream handling rather than implementing a second Responses codec.
  - Inventory must be filtered/curated so the Responses backend does not advertise a model that Cloudflare documents as unsupported by Responses.
  - _Requirements: 5,7,8,9,10,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector module tests + `make backend-plugin-cross-platform-qa` + `make backend-plugin-release-gates-static`_

- [ ] 6.2 Azure OpenAI / Azure AI Foundry connector (P)
  - Create one Azure connector artifact with distinct factory kinds only when needed to represent Azure OpenAI vs Foundry endpoint/deployment semantics; avoid two SDK implementations.
  - Responses is preferred for deployments/regions where current Azure v1 supports it; Chat is supplemental within the connector for deployments that require it.
  - Typed config owns Azure resource/endpoint, deployment/model mapping, credential mode, and any required API-version compatibility field.
  - Support API-key auth and Microsoft Entra credential chain; use Azure-supported token acquisition, never store resolved bearer tokens in YAML.
  - Inventory resolves deployment/model identities through the Azure resource/control-plane contract and maps them to stable Go-LIP canonical IDs.
  - Hard-negative test: a Responses-only route never silently falls back to Chat after the request has acquired Responses-specific required semantics.
  - Primary design source is the Azure Responses/v1 contract linked in `research.md`; no new research is expected from executor.
  - _Requirements: 5,7,8,9,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 6.3 Snowflake Cortex connector (P)
  - Typed config: account identifier, optional role, PAT/JWT credential reference; browser OAuth may be implemented in the same connector only if it uses the documented local-application flow without adding a new shared OAuth framework.
  - Construct `https://{account}.snowflakecomputing.com/api/v2/cortex/v1` internally.
  - Reuse compatible OpenAI transport where the Cortex API is wire-compatible, while keeping account/role/auth lifecycle connector-local.
  - Inventory only models/families that satisfy the coding/tool requirements frozen in the Snowflake support contract; do not advertise unrelated Cortex functions.
  - _Requirements: 5,7,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 6.4 Databricks AI connector (P)
  - Typed config: workspace host, auth token/OAuth source, optional serving endpoint/gateway selector.
  - Construct the current Databricks AI Gateway/OpenAI-compatible root from the workspace host; do not introduce provider-profile env substitution.
  - Reuse shared compatible transport; connector owns workspace authentication and serving-endpoint discovery.
  - _Requirements: 5,7,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 6.5 Infomaniak AI connector (P)
  - Typed config: `product_id` plus API-key reference.
  - Construct `https://api.infomaniak.com/2/ai/{product_id}/openai/v1` internally and reuse compatible OpenAI transport.
  - Do not add generic URL-template support to provider profiles for this one product.
  - _Requirements: 5,7,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

---

- [ ] 7. Implement provider-native and managed-cloud connectors (P)
- [ ] 7.1 Google Vertex AI connector (P)
  - Create `connectors/vertex`; this is distinct from the existing Gemini API-key backend.
  - Typed config: GCP project, location, optional explicit publisher/model/deployment selectors, credential source.
  - Use ADC/service-account/workload identity through the provider-supported Google client/auth library inside the connector.
  - Prefer the native Vertex/Gemini execution contract; do not force all Vertex models through the OpenAI compatibility surface merely for code reuse.
  - Inventory via Vertex publisher/model catalog scoped by project/location; stable canonical IDs must not depend on transient access tokens.
  - _Requirements: 5,7,8,9,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 7.2 AWS SageMaker connector (P)
  - Create `connectors/sagemaker` using AWS SDK v2 inside the connector module.
  - Typed config: AWS region/profile/credential-chain options, SageMaker endpoint name, and a declared inference contract for the selected deployment.
  - Use SigV4/default AWS credential chain and SageMaker Runtime `InvokeEndpoint`/supported streaming equivalent.
  - Enumerate configured/discoverable endpoints with SageMaker control-plane APIs, but do not claim arbitrary containers are OpenAI-compatible. Only route an endpoint after its configured contract maps canonical calls deterministically.
  - Reuse existing connector-support codecs only for explicitly compatible deployments.
  - _Requirements: 5,7,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 7.3 OCI Generative AI connector (P)
  - Create `connectors/oci` with typed region, compartment, endpoint/model selectors and OCI credential/signing source.
  - Use OCI supported request signing/workload identity; no static bearer-token conversion in common YAML.
  - Enumerate generative models/endpoints through OCI control-plane APIs; preserve stable Go-LIP IDs across generated endpoint resources.
  - _Requirements: 5,7,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 7.4 IBM watsonx.ai connector (P)
  - Create `connectors/watsonx` with typed service/base region, project or space ID, and IBM credential reference.
  - Implement IBM IAM token acquisition/refresh connector-locally and native watsonx chat/text mapping.
  - Enumerate supported foundation/deployed models with the watsonx model API and filter to language models compatible with declared Go-LIP semantics.
  - _Requirements: 5,7,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 7.5 SAP AI Core connector (P)
  - Create `connectors/sapaicore`.
  - Accept a service-key **reference** (environment/file/config secret seam consistent with connector framework), not a secret-bearing diagnostic field. Parse `clientid`, `clientsecret`, auth URL, and `serviceurls.AI_API_URL` inside the connector.
  - Acquire OAuth client-credentials token at the service-key auth URL; scope requests by configured `AI-Resource-Group` where required.
  - Discover deployments through the AI Core deployment API and map selected deployment URL/model to stable canonical IDs.
  - Reuse shared compatible transport after deployment resolution only when the selected Generative AI Hub deployment is actually compatible.
  - _Requirements: 4,5,7,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 7.6 Cohere native connector (P)
  - Create `connectors/cohere` against the native `POST /v2/chat` API rooted at `https://api.cohere.com`.
  - Implement canonical message/tool/stream mapping directly; do not route through LiteLLM or pretend the native API is OpenAI Chat.
  - Enumerate language models through Cohere's model-list API and expose only declared coding/text capabilities.
  - Add hard-negative tests for canonical semantics that Cohere cannot preserve.
  - _Requirements: 5,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

- [ ] 7.7 Replicate language-model connector (P)
  - Create `connectors/replicate`, API root `https://api.replicate.com/v1`, bearer credential `REPLICATE_API_TOKEN` or connector-standard secret reference.
  - Connector owns prediction create, stream/poll, terminal result, cancellation, and cleanup; do not hide asynchronous prediction lifecycle inside generic retry logic.
  - Enumerate models/versions via Replicate model APIs, but only expose language-model deployments with a configured/frozen mapping capable of satisfying canonical text/tool requirements. Arbitrary Replicate model schemas are out of scope.
  - Cancellation must stop polling/stream I/O and invoke provider cancellation when a prediction is cancellable.
  - _Requirements: 5,8,11_
  - _Boundary: external backend plugin_
  - _Depends: 1_
  - _Validation: connector tests + connector release gates_

---

- [ ] 8. Implement non-ACP OAuth/subscription bridges (P)
- [ ] 8.1 Establish one connector-local OAuth credential pattern without a universal auth framework
  - Reuse existing secure credential/account-store patterns from the Codex connector where they fit: restrictive files, atomic updates, redacted diagnostics, refresh-before-expiry, terminal-refresh quarantine, explicit re-login.
  - Do not add provider OAuth concepts to `pkg/lipapi` or core routing.
  - Shared helper code may live under `connector-support` only after at least two bridges need the exact same PKCE/device-code primitive; keep provider endpoints/scopes/entitlements in each connector.
  - Tests must cover state/PKCE validation, refresh, `invalid_grant`/4xx quarantine, logout/removal, and no repeated replay of a terminal refresh token.
  - _Requirements: 4,6,8,10_
  - _Boundary: connector-support/external plugins_
  - _Depends: 1_
  - _Validation: connector-support tests + connector release gates_

- [ ] 8.2 GitHub Copilot direct HTTP subscription bridge (P)
  - **Do not use Copilot ACP.** This task is direct Copilot service access only.
  - Use the current supported GitHub device/OAuth credential route and Copilot service-token exchange represented by the pinned surveyed implementations. Reference source pins: Hermes Agent commit `6dcebea7fc5d0cc4f621eeaddf52b7d877a5f882` provider/auth files and the current Cline generated `github-copilot` provider declaration.
  - Inference service identity is `https://api.githubcopilot.com`; model inventory comes from the Copilot model service/entitlement response, not a static copy of all GitHub Models.
  - If implementation confirms the direct Copilot endpoint/token exchange is no longer a documented/permitted third-party integration, mark this provider **unsupported-by-policy** and close this subtask without reverse-engineering private endpoints or using ACP as a workaround.
  - _Requirements: 6,8,11,12_
  - _Boundary: external backend plugin/auth bridge_
  - _Depends: 8.1_
  - _Validation: offline OAuth/token-exchange fixtures + connector tests_

- [ ] 8.3 GitLab Duo / Duo Agent Platform bridge (P)
  - **No ACP.** Support GitLab.com and configured self-managed instance URL.
  - Auth methods from the frozen OpenCode integration: browser OAuth or PAT; self-managed OAuth client ID is explicit config when required.
  - Support configured `GITLAB_AI_GATEWAY_URL`/Duo workflow service where the instance advertises it; dynamically discover `duo-workflow-*` models through the GitLab namespace/instance contract and cache only according to connector generation lifecycle.
  - Keep GitLab repository-management tools outside this inference backend; only model/DAP execution is in scope.
  - Reference pinned OpenCode provider docs/implementation at commit `c77100a40c16a1c7c39115023ccd6f284b476c77` (or the exact newer commit frozen by implementation branch if already vendored in research notes).
  - _Requirements: 6,7,8,11_
  - _Boundary: external backend plugin/auth bridge_
  - _Depends: 8.1_
  - _Validation: OAuth/PAT fixtures + dynamic model discovery + connector tests_

- [ ] 8.4 Claude subscription/OAuth bridge (P)
  - Keep existing `anthropic` API-key backend unchanged; this is a distinct credential/billing product.
  - Implement only Anthropic's currently documented third-party OAuth/setup-token path. Preserve the frozen Hermes distinction that consumer subscription use and API-key billing are not interchangeable.
  - Do not copy/impersonate Claude Code client identifiers solely to obtain entitlements.
  - If the current provider policy no longer permits the third-party route, record unsupported status rather than bypassing the restriction.
  - Inventory only models returned/entitled by the authenticated product.
  - _Requirements: 4,6,8,11_
  - _Boundary: external backend plugin/auth bridge_
  - _Depends: 8.1_
  - _Validation: auth-store/refresh fixtures + connector tests_

- [ ] 8.5 Nous Portal connector (P)
  - Implement the Portal subscription gateway as a distinct connector; do not conflate it with direct Nous Research API-key inference.
  - Follow the frozen Hermes reference behavior: OAuth-managed credentials, scoped `inference:invoke` JWT preferred, legacy opaque session-key only if the current public contract still requires it, automatic rotation/refresh, revoked refresh-token quarantine.
  - Include the provider-required client-identification value using truthful Go-LIP/AIProxer identity; do not claim to be Hermes.
  - Inventory the Portal model catalog dynamically and preserve provider-qualified model IDs.
  - Reference Hermes Agent commit `6dcebea7fc5d0cc4f621eeaddf52b7d877a5f882`, especially `website/docs/integrations/providers.md`, `hermes_cli/auth_commands.py`, `agent/credential_sources.py`, and Portal-related runtime code. Port semantics, not Python structure.
  - _Requirements: 4,6,7,8,11_
  - _Boundary: external backend plugin/auth bridge_
  - _Depends: 8.1_
  - _Validation: scoped-token/refresh/catalog fixtures + connector tests_

- [ ] 8.6 xAI subscription OAuth bridge (P)
  - Keep `xai` API-key Chat profile as a separate product.
  - Implement only xAI's current documented subscription OAuth/device authorization route; use the resulting bearer credential with the same xAI model service semantics supported by the authenticated account.
  - Do not assume OAuth changes the wire protocol to Responses: the API-key profile selection in this spec remains Chat unless official xAI contract changes and a separate spec revisits it.
  - Reuse token quarantine/re-auth behavior from 8.1. Entitlement 403 after successful auth is a typed entitlement failure, not an endless refresh retry.
  - Reference Hermes xAI OAuth tests/docs at pinned commit `6dcebea7fc5d0cc4f621eeaddf52b7d877a5f882` as behavioral fixtures.
  - _Requirements: 4,6,8,11_
  - _Boundary: external backend plugin/auth bridge_
  - _Depends: 8.1_
  - _Validation: device/OAuth fixtures + entitlement/error tests + connector tests_

- [ ] 8.7 Qwen Portal OAuth bridge (P)
  - Provider identity `qwen-oauth`; do not conflate with `alibaba`/DashScope API-key profiles.
  - Frozen inference base is `https://portal.qwen.ai/v1` from Hermes provider profile.
  - Port the exact Qwen Portal request adaptations represented by pinned Hermes `plugins/model-providers/qwen-oauth/__init__.py`: content normalization, system-part ephemeral cache marker only if still accepted by the current provider contract, `vl_high_resolution_images: true`, and Qwen session metadata placement.
  - Authentication is browser/PKCE external OAuth from Hermes' auth layer; do not invent a static API key requirement.
  - If any of those request adaptations require a new canonical field rather than connector-local translation, stop and escalate that semantic rather than changing `pkg/lipapi` here.
  - _Requirements: 4,6,8,11_
  - _Boundary: external backend plugin/auth bridge_
  - _Depends: 8.1_
  - _Validation: PKCE/auth fixtures + request-shape golden tests + connector tests_

- [ ] 8.8 MiniMax OAuth bridge (P)
  - Provider identity `minimax-oauth`; separate from `minimax` and `minimax-cn` API-key profiles.
  - Use global inference base `https://api.minimax.io/anthropic`; transport reuses Anthropic Messages semantics.
  - Port the frozen Hermes flow exactly from `website/docs/guides/minimax-oauth.md` at commit `6dcebea7fc5d0cc4f621eeaddf52b7d877a5f882`: generate PKCE verifier/challenge+state; POST `{base_url}/oauth/code`; display/open returned verification URI/user code; poll `{base_url}/oauth/token`; persist access+refresh+expiry; refresh within 60s of expiry using refresh-token grant; quarantine terminal 4xx/`invalid_grant`/revoked refresh tokens; explicit re-login clears quarantine.
  - Initial model inventory is `MiniMax-M2.7` and `MiniMax-M2.7-highspeed` unless authenticated enumeration provides the same/better authoritative set.
  - Do not use `MINIMAX_API_KEY` for this product.
  - _Requirements: 4,6,8,11_
  - _Boundary: external backend plugin/auth bridge_
  - _Depends: 8.1_
  - _Validation: exact OAuth state-machine fixtures + Anthropic transport tests + connector tests_

---

- [ ] 9. Finalize complete provider coverage and release evidence
- [ ] 9.1 Reconcile expected provider inventory against actual repository support
  - Enumerate current built-in backend kinds, embedded provider profiles, discovered connector manifests/factory kinds, and this spec's expected list.
  - Fail the test/release check on duplicate semantic provider ownership, missing expected profile/connector, unexpected ACP addition, or stale placeholder.
  - Do not require every optional connector artifact to be installed at runtime merely to validate the source-tree expected set; use manifest/source metadata for build-time evidence.
  - _Requirements: 9,10,12_
  - _Boundary: tests/release evidence_
  - _Depends: 3.4,5,6,7,8_
  - _Validation: focused expected-inventory test + `make backend-plugin-release-gates-static`_

- [ ] 9.2 Update the top-level supported-backend documentation
  - Update README/architecture/provider docs so they no longer under-report providers already landed (including existing Alibaba Token Plan International) and include the new profile/connector support.
  - Use compact categories plus a detailed provider document rather than making README a 100-row catalog.
  - For every connector/bridge document: include backend kind, auth method, endpoint construction, model enumeration, required config/env, protocol surface, and caveats.
  - Separate API-key and OAuth/subscription products visibly.
  - State that ACP provider work was explicitly excluded.
  - _Requirements: 7,11,12_
  - _Boundary: docs_
  - _Depends: 9.1_
  - _Validation: `make docs-check knowledge-check`_

- [ ] 9.3 Run final architecture and quality gates
  - Run full repository quality, tests, parity, example-config checks, profile scale test, and connector release/static gates.
  - Generate the change-surface report and confirm ordinary profile population did not edit canonical/core/frontend/ABI/shared-composition production surfaces.
  - Confirm no new runtime network fetch of Models.dev/Cline/OpenCode/Hermes/LiteLLM was introduced.
  - Confirm no ACP files changed as implementation of this feature except possibly tests/docs that explicitly assert ACP exclusion; production ACP changes are a failure.
  - Confirm new external connectors remain optional/closed-manifest and provider SDK dependencies do not leak into root/core packages.
  - _Requirements: 1,4,8,9,10,12_
  - _Boundary: repository-wide validation_
  - _Depends: 9.2_
  - _Validation: `make quality-checks && make test && make qa && make example-config-check && make backend-plugin-release-gates-static && make docs-check knowledge-check`_

## Parallelization Summary

- Tasks 1-5 are profile-catalog work and should be executed sequentially or in very small rebased batches because they touch the same catalog/expected-table files.
- Tasks 6.1-6.5 may run in parallel after Task 1.
- Tasks 7.1-7.7 may run in parallel after Task 1.
- Task 8.1 establishes the credential pattern; Tasks 8.2-8.8 may run in parallel after 8.1.
- Task 9 is the final convergence gate and must run after all providers intended for the release have either landed or been explicitly recorded as unsupported/deferred under the task stop rules.

## Implementation Stop Conditions

An executor must stop only the affected provider task and report the exact reason when:

- official provider API no longer matches the frozen base/protocol/auth contract;
- provider terms explicitly prohibit the required third-party subscription/OAuth use;
- implementation requires a new canonical semantic/capability or backend-plugin ABI field;
- a profile needs a new transform/URL-template/multi-secret schema feature rather than current v1 data;
- a claimed model inventory cannot be made flavor-correct through current remote or static inventory mechanisms;
- a native provider requires semantic approximation/loss instead of lossless mapping or explicit rejection.

These are not instructions to perform new broad research. They are fail-closed conditions preventing a smaller executor from inventing architecture when the frozen plan no longer matches reality.
