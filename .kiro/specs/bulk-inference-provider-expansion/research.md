# Research & Design Decisions

## Summary

This is a brownfield expansion of Go-LIP's existing provider architecture, not a new provider framework.

The central finding is that the repository already has the correct scalable mechanism for most of the requested breadth:

- `internal/providerprofiles` owns the bounded declarative `lip.provider-profile/v1` schema and embedded catalog.
- `internal/standardplugins/provider_profile_binding.go` expands `kind: provider-profile` rows into one of the certified compatible factory kinds.
- Supported profile families are OpenAI Chat, OpenAI Responses, Anthropic Messages, and OpenResponses.
- The generic family adapters already own credentials, execution, inventory, tokenizer/admission behavior, diagnostics, and canonical translation.
- Profile-only changes already have architecture/change-surface ratchets and conformance coverage.

The missing piece is mostly **catalog population**. At baseline, `internal/providerprofiles/catalog.json` contains only `example-openai-responses`. A large set of hosted vendors can therefore be added without new Go backend implementations.

The second finding is equally important: not every provider seen in Cline/OpenCode/Hermes/LiteLLM belongs in that catalog. `lip.provider-profile/v1` intentionally supports only static endpoint identity, a single supported credential environment variable, bounded headers, executable inventory policies, capability reductions, and a closed quirk vocabulary. Providers needing URL templates, multiple independent addressing/auth inputs, cloud signing, OAuth lifecycle, dynamic deployment discovery, or native non-compatible protocols must graduate to external connectors/bridges rather than widening the profile DSL casually.

## Baseline Repository Facts

### Existing built-in/provider support that must not be duplicated

- `openai-responses`
- `openai-legacy`
- `anthropic`
- `gemini`
- `bedrock`
- `alibaba-token-plan-intl` (existing dedicated Anthropic Messages execution plus OpenAI-compatible model discovery)
- compatible kinds:
  - `custom-openai-legacy-compatible`
  - `custom-openai-responses-compatible`
  - `custom-anthropic-compatible`
  - `custom-openresponses-compatible`
- existing external providers/connectors include OpenRouter, NVIDIA, Hugging Face, OpenCode Go/Zen, OpenAI Codex/app-server, CommandCode OpenAI/Anthropic, Ollama/Ollama Cloud, llama.cpp, LM Studio, vLLM, and local-stub.
- existing ACP connectors are deliberately excluded from this specification.

### Existing profile schema constraints that implementation must preserve

`lip.provider-profile/v1` currently supports:

| Concern | Executable v1 behavior |
| --- | --- |
| Families | `openai-chat-compatible`, `openai-responses-compatible`, `anthropic-compatible`, `openresponses-compatible` |
| Endpoint | literal static `https` base URL for remote services; `family_default` path policy |
| Auth | `none`, `bearer_env`, or Anthropic `api_key_env`; one env-var identifier; no literal secret |
| Inventory | family-default remote discovery or static inventory |
| Namespace | only `preserve` is executable; prefix/strip modes are rejected |
| Tokenizer | local/default supported values; no provider network tokenizer lookup |
| Safe headers | bounded allowlist for OpenAI-family profiles; auth-like headers prohibited |
| Quirks | closed enum only; arbitrary transform is rejected |
| Dialect overrides | only within certified family ceiling; OpenResponses owns its richer dialect overrides |

Do **not** turn currently rejected schema fields into a general provider scripting system as part of this work.

## Source Interpretation Rule

The four surveyed repositories serve different roles:

- **Cline** and **OpenCode** are useful for current provider naming, endpoint, conventional env-var, and client-family evidence.
- **Hermes Agent** is especially useful for coding-oriented plans/OAuth products and its Models.dev integration.
- **LiteLLM** is useful for breadth discovery only. LiteLLM frequently exposes `/chat/completions`, `/messages`, and `/responses` by translating a provider behind LiteLLM's own normalized façade. A LiteLLM check mark is **not** evidence that the upstream provider natively implements that wire API.
- **Official provider documentation/OpenAPI** wins when protocol flavor, endpoint, auth, or enumeration differs from an aggregator.

The implementation matrix below is frozen from research performed on 2026-08-28. Executors should not redo broad research. If an upstream contradicts a frozen row during implementation, record the contradiction and stop only that provider task.

## Mechanical Protocol-Selection Algorithm

Implementation agents must follow this decision tree exactly:

1. Check current branch for equivalent existing support. If present, do not duplicate it.
2. If official/current evidence establishes native OpenAI Responses and Responses covers the complete intended coding/text model set, create **one unsuffixed Responses profile**.
3. If Responses exists but covers only a strict subset, create `<provider>-responses` for that subset and a supplemental `<provider>-openai` or `<provider>-anthropic` for models not executable through Responses. Document `-responses` as preferred.
4. If no native Responses is established and a static-base bearer OpenAI Chat endpoint fits profile v1, create one unsuffixed Chat profile.
5. If Anthropic Messages is the provider's only/preferred compatible API and its base/auth fit v1, create one unsuffixed Anthropic profile.
6. If distinct model populations require distinct API flavors, suffix **all** identities for that provider with `-responses`, `-openai`, `-anthropic`, or `-openresponses`.
7. Do not create multiple profiles merely because the same models can be reached through several equivalent schemas. Prefer Responses; otherwise choose the provider's best documented coding-agent path.
8. Use `family_default` inventory only if that flavor's model-list endpoint will not advertise unusable models. Otherwise use a static flavor-specific inventory.
9. If static endpoint + one supported secret cannot represent the product, stop trying to force it into a profile and use the connector/bridge matrix.

## Locked High-Value / Multi-Flavor Decisions

| Go-LIP identity | Family | Preferred | Go-LIP base URL | Credential env | Inventory | Locked rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `deepseek-responses` | Responses | yes | `https://api.deepseek.com` | `DEEPSEEK_API_KEY` | static: `deepseek-v4-flash` | Official Responses API currently supports V4 Flash but not V4 Pro. Never let generic `/models` over-advertise V4 Pro on this identity. |
| `deepseek-openai` | Chat | supplemental | `https://api.deepseek.com` | `DEEPSEEK_API_KEY` | family default `/models` if conformance confirms response shape; otherwise static Flash + Pro | Full OpenAI-format route for V4 Flash/Pro. No third duplicate Anthropic profile: current Anthropic surface does not unlock a distinct required model population. |
| `fireworks` | Responses | yes | `https://api.fireworks.ai/inference/v1` | `FIREWORKS_API_KEY` | family default `/models` only after endpoint-contract test; otherwise frozen static text-model inventory | Fireworks officially exposes Responses, Chat, and Anthropic Messages. Use Responses as the single first-class default because it is a native Fireworks endpoint; do not duplicate equivalent Chat/Messages profiles without a model-coverage need. |
| `groq` | Responses | yes | `https://api.groq.com/openai/v1` | `GROQ_API_KEY` | family default | Groq documents that all current models support Responses; built-in server tools remain model-specific and are not implied by the profile. |
| `digitalocean` | Responses | yes | `https://inference.do-ai.run/v1` | `DIGITALOCEAN_ACCESS_TOKEN` | family default `/models` | Serverless inference has `/v1/responses` and `/v1/models`. OAuth-discovered Inference Routers are a distinct connector/control-plane concern, not part of this profile. |
| `scaleway-responses` | Responses | yes | `https://api.scaleway.ai/v1` | `SCW_SECRET_KEY` | static initially; include models explicitly validated for Responses, including `openai/gpt-oss-120b:fp4` and `openai/gpt-oss-20b:fp4` when present in serverless catalog | Scaleway exposes `/v1/responses`, but model endpoint support is model-specific. Keep this identity flavor-correct. |
| `scaleway-openai` | Chat | supplemental | `https://api.scaleway.ai/v1` | `SCW_SECRET_KEY` | family default `/models` | Broad Chat-compatible serverless model access for models not safely exposed by the Responses profile. |
| `vercel-ai-gateway` | Responses | yes | `https://ai-gateway.vercel.sh/v1` | `AI_GATEWAY_API_KEY` | family default `/models` | Vercel documents Chat, Responses, Anthropic Messages, and OpenResponses. Responses is the default Go-LIP surface; separate equivalent profiles add no required model coverage. |
| `requesty` | Responses | yes | `https://router.requesty.ai/v1` | `REQUESTY_API_KEY` | family default `/models` | Requesty explicitly translates Responses across its model library and exposes `/v1/models`. Use Responses by default; Requesty's `openai-responses/` prefix remains available as a model ID when native OpenAI routing is desired. |
| `kilo` | Responses | yes | `https://api.kilo.ai/api/gateway` | `KILO_API_KEY` | static/curated from frozen Kilo catalog unless `/models` conformance passes | Cline's current product override explicitly routes Kilo through OpenAI Responses. |
| `meta` | Responses | yes | `https://api.meta.ai/v1` | `META_MODEL_API_KEY` | family default if `/models` conforms | Meta currently documents a Responses `create-response` surface; Cline classifies the Meta Model API in its OpenAI/Responses family. |
| `xai` | Chat | yes | `https://api.x.ai/v1` | `XAI_API_KEY` | family default `/models` | Current official xAI OpenAPI exposes `/v1/chat/completions` but no `/v1/responses`. Do **not** promote xAI to Responses based on Hermes/LiteLLM translation claims. SuperGrok OAuth is separate bridge work. |

### Official evidence for the decisions above

- DeepSeek Responses: <https://api-docs.deepseek.com/guides/responses_api/>; model/pricing matrix: <https://api-docs.deepseek.com/quick_start/pricing/>
- Fireworks Responses: <https://docs.fireworks.ai/api-reference/post-responses>; Anthropic compatibility: <https://docs.fireworks.ai/tools-sdks/anthropic-compatibility>
- Groq Responses: <https://console.groq.com/docs/responses-api>
- DigitalOcean Responses: <https://docs.digitalocean.com/products/inference/how-to/use-responses-api/>
- Scaleway API endpoints: <https://www.scaleway.com/en/developers/api/generative-apis>; model list: <https://www.scaleway.com/en/docs/generative-apis/api-cli/using-models-api/>
- Vercel APIs: <https://vercel.com/docs/ai-gateway/sdks-and-apis>; model list: <https://vercel.com/docs/ai-gateway/models-and-providers>
- Requesty Responses: <https://docs.requesty.ai/api-reference/endpoint/responses-create>; models: <https://docs.requesty.ai/api-reference/endpoint/models-list>
- xAI current OpenAPI: <https://api.x.ai/api-docs/openapi.json>

## Locked OpenAI Chat Profile Matrix

Unless a row above overrides protocol choice, the following are straightforward `openai-chat-compatible` profile candidates. For these rows, `models: { discovery: family_default, namespace: { mode: preserve } }` is the default implementation instruction. If the provider's `/models` endpoint fails the existing OpenAI discovery contract in an offline characterization fixture, switch that row to a frozen static inventory; do **not** add a new parser casually.

Rows with an existing equivalent Go-LIP connector are intentionally omitted.

| Profile ID | Base URL | Env var | Notes |
| --- | --- | --- | --- |
| `302ai` | `https://api.302.ai/v1` | `302AI_API_KEY` | Models.dev/Cline OpenAI-compatible |
| `abacus` | `https://routellm.abacus.ai/v1` | `ABACUS_API_KEY` | Models.dev/Cline |
| `abliteration-ai` | `https://api.abliteration.ai/v1` | `ABLIT_KEY` | Models.dev/Cline |
| `ai-router` | `https://api.ai-router.dev/v1` | `AI_ROUTER_API_KEY` | Broker |
| `aiand` | `https://api.aiand.com/v1` | `AIAND_API_KEY` | Broker |
| `aihubmix` | `https://api.aihubmix.com/v1` | `AIHUBMIX_API_KEY` | Use Cline product override URL, not blank generated URL |
| `aki-io` | `https://aki.io/v1` | `AKI_IO_API_KEY` | Models.dev/Cline |
| `alibaba` | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` | `DASHSCOPE_API_KEY` | International Model Studio |
| `alibaba-cn` | `https://dashscope.aliyuncs.com/compatible-mode/v1` | `DASHSCOPE_API_KEY` | China Model Studio |
| `alibaba-coding-plan` | `https://coding-intl.dashscope.aliyuncs.com/v1` | `ALIBABA_CODING_PLAN_API_KEY` | International Coding Plan |
| `alibaba-coding-plan-cn` | `https://coding.dashscope.aliyuncs.com/v1` | `ALIBABA_CODING_PLAN_API_KEY` | China Coding Plan |
| `alibaba-token-plan-cn` | `https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1` | `ALIBABA_TOKEN_PLAN_API_KEY` | Do not add an Intl duplicate: Go-LIP already has `alibaba-token-plan-intl` dedicated support for that product |
| `ambient` | `https://api.ambient.xyz/v1` | `AMBIENT_API_KEY` | Models.dev/Cline |
| `amd` | `https://developer.amd.com.cn/radeon/api/v1` | `AMD_API_KEY` | AMD token factory |
| `anyapi` | `https://api.anyapi.ai/v1` | `ANYAPI_API_KEY` | Broker |
| `arcee` | `https://api.arcee.ai/api/v1` | `ARCEE_API_KEY` | Hosted inference |
| `auriko` | `https://api.auriko.ai/v1` | `AURIKO_API_KEY` | Broker |
| `baseten` | `https://inference.baseten.co/v1` | `BASETEN_API_KEY` | Serverless inference only; arbitrary dedicated URLs remain custom-compatible/operator-configured |
| `berget` | `https://api.berget.ai/v1` | `BERGET_API_KEY` | Models.dev/Cline |
| `blueclaw` | `https://openai.blueclaw.network/v1` | `BLUECLAW_API_KEY` | Models.dev/Cline |
| `cerebras` | `https://api.cerebras.ai/v1` | `CEREBRAS_API_KEY` | If upstream `/models` endpoint differs, use static catalog rather than parser changes |
| `chutes` | `https://llm.chutes.ai/v1` | `CHUTES_API_KEY` | Hosted/TEE model catalog |
| `clarifai` | `https://api.clarifai.com/v2/ext/openai/v1` | `CLARIFAI_PAT` | OpenAI compatibility layer |
| `claudinio` | `https://api.claudin.io/v1` | `CLAUDINIO_API_KEY` | Broker |
| `cline-pass` | `https://api.cline.bot/api/v1` | `CLINE_API_KEY` | Cline subscription API; API-key product, not ACP |
| `cloudferro-sherlock` | `https://api-sherlock.cloudferro.com/openai/v1` | `CLOUDFERRO_SHERLOCK_API_KEY` | EU hosted |
| `coralbricks` | `https://inference.coralbricks.ai/v1` | `CORAL_API_KEY` | Hosted inference |
| `cortecs` | `https://api.cortecs.ai/v1` | `CORTECS_API_KEY` | `/models` documented at same root |
| `crof` | `https://crof.ai/v1` | `CROF_API_KEY` | Broker |
| `crossmodel` | `https://api.crossmodel.ai/v1` | `CROSSMODEL_API_KEY` | Broker |
| `crusoe` | `https://api.inference.crusoecloud.com/v1` | `CRUSOE_API_KEY` | Managed inference |
| `daoxe` | `https://daoxe.com/v1` | `DAOXE_API_KEY` | Broker |
| `deepinfra` | `https://api.deepinfra.com/v1/openai` | `DEEPINFRA_API_KEY` | OpenAI compatibility surface only |
| `dinference` | `https://api.dinference.com/v1` | `DINFERENCE_API_KEY` | Hosted inference |
| `drun` | `https://chat.d.run/v1` | `DRUN_API_KEY` | China hosted inference |
| `ebcloud` | `https://maas-api.ebcloud.com/v1` | `EBCLOUD_API_KEY` | Hosted inference |
| `echo` | `https://echo.tracerml.ai/v1` | `ECHO_API_KEY` | OpenAI-compatible service |
| `edenai` | `https://api.edenai.run/v3` | `EDENAI_API_KEY` | Broker; verify `/models` fixture before family-default inventory |
| `empiriolabs` | `https://api.empiriolabs.ai/v1` | `EMPIRIOLABS_API_KEY` | Broker |
| `evroc` | `https://models.think.evroc.com/v1` | `EVROC_API_KEY` | EU provider |
| `fastrouter` | `https://go.fastrouter.ai/api/v1` | `FASTROUTER_API_KEY` | Broker |
| `friendli` | `https://api.friendli.ai/serverless/v1` | `FRIENDLI_TOKEN` | Serverless only |
| `frogbot` | `https://app.frogbot.ai/api/v1` | `FROGBOT_API_KEY` | Broker |
| `gmicloud` | `https://api.gmi-serving.com/v1` | `GMICLOUD_API_KEY` | Hosted inference |
| `greenpt` | `https://api.greenpt.ai/v1` | `GREENPT_API_KEY` | Hosted inference |
| `helicone` | `https://ai-gateway.helicone.ai/v1` | `HELICONE_API_KEY` | Gateway-specific optional headers are not required for base support |
| `hetzner` | `https://inference.hetzner.com/api/v1` | `HETZNER_API_KEY` | Hosted inference |
| `hpc-ai` | `https://api.hpc-ai.com/inference/v1` | `HPC_AI_API_KEY` | Hosted inference |
| `hyper` | `https://hyper.charm.land/v1` | `HYPER_API_KEY` | Hosted inference |
| `iflowcn` | `https://apis.iflow.cn/v1` | `IFLOW_API_KEY` | China provider |
| `impossibl` | `https://api.impossibl.com/v1` | `IMPOSSIBL_API_KEY` | Broker |
| `inception` | `https://api.inceptionlabs.ai/v1` | `INCEPTION_API_KEY` | Hosted inference |
| `inceptron` | `https://api.inceptron.io/v1` | `INCEPTRON_API_KEY` | Hosted inference |
| `inference-net` | `https://inference.net/v1` | `INFERENCE_API_KEY` | Use Go-LIP ID `inference-net` to avoid generic word collision |
| `inferx` | `https://model.inferx.net/endpoints/v1` | `INFERX_API_KEY` | Hosted inference |
| `io-net` | `https://api.intelligence.io.solutions/api/v1` | `IOINTELLIGENCE_API_KEY` | IO.NET Intelligence |
| `jalapeno` | `https://api.jalapeno-cloud.ai/v1` | `JALAPENO_API_KEY` | Hosted inference |
| `jiekou` | `https://api.jiekou.ai/openai` | `JIEKOU_API_KEY` | OpenAI compatibility root |
| `kenari` | `https://kenari.id/v1` | `KENARI_API_KEY` | Hosted inference |
| `llmgateway` | `https://api.llmgateway.io/v1` | `LLMGATEWAY_API_KEY` | DevPass/LLM Gateway; use one identity for duplicate upstream aliases |
| `llmtech` | `https://api.llmtech.eu/v1` | `LLMTECH_API_KEY` | EU hosted |
| `llmtr` | `https://llmtr.com/v1` | `LLMTR_API_KEY` | Hosted inference |
| `longcat` | `https://api.longcat.chat/openai` | `LONGCAT_API_KEY` | OpenAI compatibility root |
| `lucidquery` | `https://api.lucidquery.com/v1` | `LUCIDQUERY_API_KEY` | Hosted inference |
| `meganova` | `https://api.meganova.ai/v1` | `MEGANOVA_API_KEY` | Broker |
| `mistral` | `https://api.mistral.ai/v1` | `MISTRAL_API_KEY` | Use only after compatible-family fixture certifies tool/reasoning shape; otherwise graduate to dedicated family adapter |
| `mixlayer` | `https://models.mixlayer.ai/v1` | `MIXLAYER_API_KEY` | Broker |
| `moark` | `https://moark.com/v1` | `MOARK_API_KEY` | Hosted inference |
| `modal` | `https://inference.us-west.modal.direct/v1` | `MODAL_PROXY_TOKEN` | Shared endpoint/proxy-token path only |
| `model-oracle-ai` | `https://api.modeloracle.com/api/v1` | `MODEL_ORACLE_API_KEY` | Broker |
| `modelis` | `https://modelishub.com/v1` | `MODELIS_API_KEY` | Broker |
| `modelscope` | `https://api-inference.modelscope.cn/v1` | `MODELSCOPE_API_KEY` | China model service |
| `moonshot` | `https://api.moonshot.ai/v1` | `MOONSHOT_API_KEY` | International pay-as-you-go |
| `moonshot-cn` | `https://api.moonshot.cn/v1` | `MOONSHOT_API_KEY` | China pay-as-you-go |
| `morph` | `https://api.morphllm.com/v1` | `MORPH_API_KEY` | OpenAI-compatible inference |
| `neuralwatt` | `https://api.neuralwatt.com/v1` | `NEURALWATT_API_KEY` | Broker |
| `nova` | `https://api.nova.amazon.com/v1` | `NOVA_API_KEY` | Amazon Nova direct API; distinct from Bedrock |
| `novita-ai` | `https://api.novita.ai/openai` | `NOVITA_API_KEY` | OpenAI compatibility root |
| `ofox` | `https://api.ofox.ai/v1` | `OFOX_API_KEY` | Broker |
| `opper` | `https://api.opper.ai/v3/compat` | `OPPER_API_KEY` | Compatibility endpoint |
| `orcarouter` | `https://api.orcarouter.ai/v1` | `ORCAROUTER_API_KEY` | Broker |
| `ovhcloud` | `https://oai.endpoints.kepler.ai.cloud.ovh.net/v1` | `OVHCLOUD_API_KEY` | OVHcloud AI Endpoints |
| `pendra` | `https://api.pendra.ai/api/v1` | `PENDRA_API_KEY` | Broker |
| `pioneer` | `https://api.pioneer.ai/v1` | `PIONEER_API_KEY` | Hosted inference |
| `poe` | `https://api.poe.com/v1` | `POE_API_KEY` | Poe OpenAI-compatible API |
| `poolside` | `https://inference.poolside.ai/v1` | `POOLSIDE_API_KEY` | Hosted Poolside; customer-specific deployments remain custom-compatible |
| `qihang-ai` | `https://api.qhaigc.net/v1` | `QIHANG_API_KEY` | Hosted inference |
| `qiniu-ai` | `https://api.qnaigc.com/v1` | `QINIU_API_KEY` | Hosted inference |
| `regolo-ai` | `https://api.regolo.ai/v1` | `REGOLO_API_KEY` | EU hosted |
| `routing-run` | `https://api.routing.run/v1` | `ROUTING_RUN_API_KEY` | Broker |
| `scnet-token-plan` | `https://api.scnet.cn/api/llm/v1` | `SCNET_API_KEY` | China token plan |
| `scx-ai` | `https://api.scx.ai/v1` | `SCX_API_KEY` | Australian sovereign provider |
| `siliconflow` | `https://api.siliconflow.com/v1` | `SILICONFLOW_API_KEY` | Global |
| `siliconflow-cn` | `https://api.siliconflow.cn/v1` | `SILICONFLOW_CN_API_KEY` | China |
| `stackit` | `https://api.openai-compat.model-serving.eu01.onstackit.cloud/v1` | `STACKIT_API_KEY` | EU sovereign platform |
| `standardcompute` | `https://api.stdcmpt.com/v1` | `STANDARDCOMPUTE_API_KEY` | Hosted inference |
| `stepfun` | `https://api.stepfun.ai/v1` | `STEPFUN_API_KEY` | Global |
| `stepfun-cn` | `https://api.stepfun.com/v1` | `STEPFUN_API_KEY` | China |
| `stepfun-step-plan` | `https://api.stepfun.ai/step_plan/v1` | `STEPFUN_API_KEY` | Global Step Plan |
| `stepfun-step-plan-cn` | `https://api.stepfun.com/step_plan/v1` | `STEPFUN_API_KEY` | China Step Plan |
| `submodel` | `https://llm.submodel.ai/v1` | `SUBMODEL_INSTAGEN_ACCESS_KEY` | Hosted inference |
| `synthetic` | `https://api.synthetic.new/openai/v1` | `SYNTHETIC_API_KEY` | Broker |
| `tencent-coding-plan` | `https://api.lkeap.cloud.tencent.com/coding/v3` | `TENCENT_CODING_PLAN_API_KEY` | China Coding Plan |
| `tencent-token-plan` | `https://api.lkeap.cloud.tencent.com/plan/v3` | `TENCENT_TOKEN_PLAN_API_KEY` | Token Plan |
| `tencent-tokenhub` | `https://tokenhub.tencentmaas.com/v1` | `TENCENT_TOKENHUB_API_KEY` | TokenHub |
| `tensorx` | `https://api.tensorx.ai/v1` | `TENSORX_API_KEY` | Hosted inference |
| `the-grid-ai` | `https://api.thegrid.ai/v1` | `THEGRID_API_KEY` | Hosted inference |
| `tinfoil` | `https://inference.tinfoil.sh/v1` | `TINFOIL_API_KEY` | Confidential inference |
| `together` | `https://api.together.xyz/v1` | `TOGETHER_API_KEY` | Serverless hosted models |
| `trustedrouter` | `https://api.trustedrouter.com/v1` | `TRUSTEDROUTER_API_KEY` | Broker |
| `vultr` | `https://api.vultrinference.com/v1` | `VULTR_API_KEY` | Hosted inference |
| `wafer-ai` | `https://pass.wafer.ai/v1` | `WAFER_API_KEY` | Wafer Pass |
| `wandb` | `https://api.inference.wandb.ai/v1` | `WANDB_API_KEY` | W&B Inference |
| `xiaomi` | `https://api.xiaomimimo.com/v1` | `XIAOMI_API_KEY` | MiMo API |
| `xiaomi-token-plan-eu` | `https://token-plan-ams.xiaomimimo.com/v1` | `XIAOMI_API_KEY` | Europe |
| `xiaomi-token-plan-cn` | `https://token-plan-cn.xiaomimimo.com/v1` | `XIAOMI_API_KEY` | China |
| `xiaomi-token-plan-sg` | `https://token-plan-sgp.xiaomimimo.com/v1` | `XIAOMI_API_KEY` | Singapore |
| `xpersona` | `https://www.xpersona.co/v1` | `XPERSONA_API_KEY` | Broker |
| `zai` | `https://api.z.ai/api/paas/v4` | `ZHIPU_API_KEY` | Global GLM API |
| `zai-cn` | `https://open.bigmodel.cn/api/paas/v4` | `ZHIPU_API_KEY` | China GLM API |
| `zai-coding-plan` | `https://api.z.ai/api/coding/paas/v4` | `ZHIPU_API_KEY` | Global coding plan |
| `zai-coding-plan-cn` | `https://open.bigmodel.cn/api/coding/paas/v4` | `ZHIPU_API_KEY` | China coding plan |
| `zeldoc` | `https://api.zeldoc.ai/v1` | `ZELDOC_API_KEY` | Hosted inference |
| `zenifra` | `https://ai.zenifra.com/v1` | `ZENIFRA_AI_KEY` | Hosted inference |
| `zenmux` | `https://zenmux.ai/api/v1` | `ZENMUX_API_KEY` | Broker |

### Rows deliberately excluded from profile v1 despite appearing compatible in surveyed catalogs

| Provider | Why profile v1 is insufficient | Planned treatment |
| --- | --- | --- |
| Cloudflare AI Gateway / Workers AI | account ID is part of base URL; optional gateway ID header; one token plus account identity; multi-flavor model-dependent coverage | external connector using current REST API, Responses preferred |
| Databricks | workspace host plus token form a dynamic URL; deployment/catalog semantics | connector |
| Infomaniak | product ID is part of URL in addition to API key | connector or narrowly reusable parameterized endpoint facility only if another provider justifies it |
| Snowflake Cortex | account identifier in URL; PAT/JWT/browser OAuth; optional role | connector |
| Privatemode AI | endpoint itself is operator-specific env/config in addition to key | keep custom-compatible for arbitrary endpoints; no fake first-class static profile until a stable hosted endpoint exists |
| Bailing | surveyed generated URL is already the full `/chat/completions` endpoint, not a reusable base URL for Go-LIP's family path join | defer pending verified compatible base root; do not guess |
| GitHub Copilot | service token lifecycle/entitlement differs from ordinary bearer API keys | OAuth/subscription bridge, non-ACP only |

## Locked Anthropic-Compatible Profiles

Remember that Go-LIP's Anthropic compatible adapter appends `/v1/messages`. Store the **Anthropic SDK base URL**, not a full `/v1/messages` endpoint.

| Profile ID | Go-LIP base URL | Env var | Inventory | Notes |
| --- | --- | --- | --- | --- |
| `kimi-coding` | `https://api.kimi.com/coding/` | `KIMI_API_KEY` | static: `k3`, `k3-256k`, `kimi-for-coding`, `kimi-for-coding-highspeed` | Official Kimi Code docs say OpenAI and Anthropic protocols expose the same four models. Choose Anthropic as the single first-class profile because Kimi explicitly documents it for Claude-style coding tools; do not create duplicate OpenAI profile. Preserve real client identity; Kimi forbids tampering with client identifiers. |
| `minimax` | `https://api.minimax.io/anthropic` | `MINIMAX_API_KEY` | family default `/v1/models` | Official Anthropic-compatible `/anthropic/v1/models` endpoint exists. |
| `minimax-cn` | `https://api.minimaxi.com/anthropic` | `MINIMAX_API_KEY` | family default if China `/anthropic/v1/models` fixture matches; otherwise static mirror of allowed China catalog | Separate regional commercial service. |
| `thinking-machines` | `https://tinker.thinkingmachines.dev/services/tinker-prod/anthropic/api` | `TINKER_API_KEY` | static initial `thinkingmachines/Inkling`; arbitrary user checkpoint paths are not enumerable catalog rows | Official Tinker Anthropic endpoint appends `/v1/messages`. Mark beta/low-throughput caveat in docs and disable prompt-cache capability because Tinker states `cache_control` is ignored. |
| `freemodel` | `https://cc.freemodel.dev` | `FREEMODEL_API_KEY` | static or custom `/v1/models` only after fixture | Cline/Models.dev classifies as Anthropic. Include only if endpoint behavior passes profile TCK without new quirk. |

MiniMax official references:

- Anthropic chat: <https://platform.minimax.io/docs/api-reference/text-chat-anthropic>
- Anthropic model enumeration: <https://platform.minimax.io/docs/api-reference/models/anthropic/list-models>

Kimi official reference: <https://www.kimi.com/code/docs/en/>

Tinker official reference: <https://tinker-docs.thinkingmachines.ai/tinker/compatible-apis/anthropic/>

## Connector / Bridge Matrix

These are not profile rows. Each implementation must be an optional external connector/bridge unless an existing reusable connector-support package already owns the exact common behavior.

| Planned identity | API/transport | Endpoint/addressing | Auth | Enumeration | Explicit implementation guidance |
| --- | --- | --- | --- | --- | --- |
| `cloudflare-ai-gateway` | Responses preferred; Chat/Messages also offered | `https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1` | Cloudflare API token + account ID; optional `cf-aig-gateway-id` | Cloudflare model catalog/control plane | Config fields: account ID, token/env, optional gateway ID. Do not use deprecated universal endpoint for new integration. Filter inventory by selected protocol/model capability where required. |
| `azure-openai` / `azure-foundry` | OpenAI Responses preferred where deployment/region supports it; Chat fallback | resource/deployment-specific Azure v1 endpoints | API key or Microsoft Entra ID | deployed-model/resource APIs | Use Azure identity/provider SDK or explicit token source inside connector; never make resource name a provider-profile template. Keep deployment ID distinct from model display ID. |
| `vertex` | Google/Vertex native execution; optional OpenAI-compatible Chat is not the primary architecture | project + location + publisher/model endpoint | ADC/service account/workload identity; API-key path only where Google supports it | Vertex publisher/model catalog | Existing Gemini backend is not Vertex. Connector owns project/location and ADC lifecycle. Reuse canonical Gemini translation only where semantically correct, not by URL spoofing. |
| `sagemaker` | SageMaker Runtime `InvokeEndpoint` / streaming equivalent | region + endpoint name | AWS SigV4/default chain | SageMaker `ListEndpoints` + endpoint metadata | Use AWS SDK in connector. Payload shape is deployment-specific; support only deployments configured with a defined compatible inference contract rather than pretending every SageMaker container is OpenAI-compatible. |
| `oci-generative-ai` | OCI Generative AI inference API | region, compartment, optional custom endpoint | OCI request signing/workload identity | OCI model/endpoint listing | Connector owns region/compartment/signing. |
| `watsonx` | IBM watsonx native chat/text | regional watsonx service + project/space | IBM IAM | watsonx foundation models/deployments | Connector owns IAM token lifecycle and project/space scoping. |
| `sap-ai-core` | SAP Generative AI Hub / deployment APIs | service key supplies `AI_API_URL`; selected deployment URL | OAuth client credentials or supported service-key certificate | AI Core deployments/resource groups | Parse service-key JSON in connector credential layer, obtain OAuth token, discover/select deployment, send resource-group header as required. |
| `snowflake-cortex` | Cortex OpenAI-compatible surface | `https://{account}.snowflakecomputing.com/api/v2/cortex/v1` | PAT/JWT; browser OAuth is optional later phase | Cortex catalog | Account/role are config, not env interpolation in profile. |
| `databricks-ai` | Databricks AI Gateway/OpenAI-compatible | workspace-specific `https://{host}/ai-gateway/mlflow/v1` | Databricks token/OAuth | workspace serving endpoints/models | Host + token config and workspace identity justify connector. |
| `infomaniak-ai` | OpenAI-compatible | product-specific `https://api.infomaniak.com/2/ai/{product_id}/openai/v1` | API key + product ID | product model list | Simple connector; no generic URL-template schema extension. |
| `cohere` | Cohere native `POST /v2/chat` | `https://api.cohere.com` | Cohere bearer/API token | Cohere model API | Native adapter/connector. Do not route through LiteLLM translation. |
| `replicate` | prediction resource API | `https://api.replicate.com/v1` | bearer `REPLICATE_API_TOKEN` | `GET /v1/models`, model versions | Connector owns create/poll/stream/cancel lifecycle and model-specific schema restrictions. Only language models that can satisfy canonical text/tool contract should be routable. |
| `github-copilot` | direct Copilot subscription HTTP | `https://api.githubcopilot.com` family | GitHub device/OAuth then Copilot service token lifecycle | Copilot model service | **No ACP.** Reuse secure OAuth/account patterns; support only documented entitlement route. |
| `gitlab-duo` | Duo/DAP workflow APIs | GitLab.com or configured self-managed instance; optional AI Gateway/DWS URL | OAuth or PAT | dynamic Duo/DAP model discovery | **No ACP.** Connector/bridge owns instance URL and workflow-service semantics. |
| `claude-subscription` | Anthropic/Claude Code subscription credential path | provider-managed | documented OAuth/setup-token flow only | entitled Claude models | Separate from existing `anthropic` API-key backend. Do not claim base Max allowance if upstream says third-party path uses extra usage. |
| `nous-portal` | Nous subscription gateway | provider-managed Portal gateway | OAuth + rotating scoped JWT | Portal catalog | Preserve Hermes-observed scoped `inference:invoke` JWT lifecycle pattern conceptually; implement against public/current contract only. |
| `xai-oauth` | xAI model API after subscription OAuth | xAI managed | SuperGrok/X subscription OAuth | xAI model catalog | Separate credential product from `xai` API-key Chat profile. |
| `qwen-oauth` | Qwen coding subscription | provider managed | browser PKCE/OAuth | entitled Qwen catalog | Separate from DashScope API-key profiles. |
| `minimax-oauth` | MiniMax coding subscription | provider managed | browser PKCE/OAuth | entitled MiniMax catalog | Separate from `minimax` API-key Anthropic profile. |

Primary connector references:

- Azure Responses/auth: <https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/responses>
- Vertex OpenAI compatibility/background: <https://cloud.google.com/vertex-ai/generative-ai/docs/multimodal/call-vertex-using-openai-library>
- SageMaker InvokeEndpoint/SigV4: <https://docs.aws.amazon.com/sagemaker/latest/APIReference/API_runtime_InvokeEndpoint.html>
- OCI Generative AI API index: <https://docs.oracle.com/en-us/iaas/api/>
- SAP service key/token mechanics: <https://help.sap.com/docs/sap-ai-core/sap-ai-core-service-guide/use-service-key>
- Cohere native chat: <https://docs.cohere.com/v2/reference/chat>
- Replicate HTTP API: <https://replicate.com/docs/reference/http/>
- Cloudflare REST API: <https://developers.cloudflare.com/ai-gateway/usage/rest-api/>

## Catalog Data Shape for Executors

A normal OpenAI Chat profile should look mechanically like:

```json
{
  "api_version": "lip.provider-profile/v1",
  "id": "together",
  "family": "openai-chat-compatible",
  "endpoint": {
    "base_url": "https://api.together.xyz/v1",
    "path_policy": "family_default"
  },
  "auth": {
    "mode": "bearer_env",
    "env_var": "TOGETHER_API_KEY"
  },
  "models": {
    "discovery": "family_default",
    "namespace": {"mode": "preserve"}
  },
  "tokenizer": {
    "id": "cl100k_base",
    "source": "local_tokenizer"
  }
}
```

A normal Responses profile differs only by `family: "openai-responses-compatible"`.

A normal Anthropic profile uses the SDK base root and `api_key_env`:

```json
{
  "api_version": "lip.provider-profile/v1",
  "id": "minimax",
  "family": "anthropic-compatible",
  "endpoint": {
    "base_url": "https://api.minimax.io/anthropic",
    "path_policy": "family_default"
  },
  "auth": {
    "mode": "api_key_env",
    "env_var": "MINIMAX_API_KEY"
  },
  "models": {
    "discovery": "family_default",
    "namespace": {"mode": "preserve"}
  },
  "tokenizer": {
    "id": "cl100k_base",
    "source": "local_tokenizer"
  }
}
```

Operator config is intentionally short:

```yaml
plugins:
  backends:
    - id: together
      kind: provider-profile
      enabled: true
      config:
        profile: together
```

The profile binding derives `backend_prefix=together`, base URL, credential env root, tokenizer, and inventory policy. Do not duplicate those values into every example unless the document is explaining the expansion result.

## Brownfield Gap Analysis and Requirement Repairs

### Gap 1: Embedded catalog is placeholder-only

**Finding:** The scalable profile machinery exists, but `catalog.json` contains only an example row.

**Repair:** Requirements explicitly make real catalog population and placeholder removal part of the feature. No new registry is designed.

### Gap 2: Profile v1 contains schema symbols for behaviors intentionally not executable

**Finding:** namespace prefix/strip and disabled discovery are represented by types but currently rejected; arbitrary transforms are also rejected.

**Repair:** The spec does not ask executors to enable them. Any provider needing those behaviors is reclassified to static inventory, a narrow shared-family change, or connector work.

### Gap 3: Model enumeration can over-advertise models for a selected flavor

**Finding:** generic OpenAI discovery reads `{data:[{id}]}` but does not know per-model endpoint compatibility. DeepSeek is the concrete case: current `/responses` supports V4 Flash but not V4 Pro.

**Repair:** Requirement 3 mandates static flavor-specific inventories whenever provider-wide `/models` is broader than the selected wire flavor. DeepSeek is locked to this pattern.

### Gap 4: Survey sources can lie by translation

**Finding:** LiteLLM's endpoint support matrix reports what LiteLLM can expose after translation, not what upstream vendors natively speak.

**Repair:** Requirement 2.6 and Requirement 8.6 prohibit deriving native Responses/Messages support from LiteLLM alone. xAI is explicitly kept on Chat because its current official OpenAPI has no `/v1/responses` despite secondary-source claims.

### Gap 5: Dynamic endpoint products do not fit one-secret static profiles

**Finding:** Cloudflare, Databricks, Infomaniak, Snowflake and managed clouds need account/project/host/deployment identity in addition to a secret.

**Repair:** These are connector tasks. The spec intentionally does not widen profile v1 into a URL-template or multi-secret DSL.

### Gap 6: Multi-protocol naming could become ambiguous

**Finding:** A bare provider ID plus multiple suffixed variants would leave unclear which model population and default semantics the bare identity represents.

**Repair:** If multiple identities are required, all are protocol-suffixed. If only one is required, use the bare ID. Responses is documented as preferred whenever present.

## Risks & Mitigations

| Risk | Mitigation |
| --- | --- |
| Huge catalog PR hides bad URLs | deterministic batches + expected-ID manifest/tests + profile-only checks per batch |
| Provider docs change during implementation | freeze matrix; stop only contradictory provider task; never improvise architecture |
| `/models` over-advertises incompatible models | static flavor-specific inventory |
| New provider package explosion | profile-first decision tree + architecture ratchet |
| Runtime dependency on Models.dev | embedded reviewed catalog only; no runtime fetch |
| Secret/header leakage | existing profile validation + connector secret diagnostics rules |
| OAuth bridges violate provider terms | only documented public flows; no client spoofing or entitlement bypass |
| Optional connectors bloat root binary | external closed-manifest connectors, provider SDKs isolated to connector modules |
| Smaller executor invents a new abstraction | tasks name exact files, patterns, gates, and stop conditions; no implementation-time research assignment |

## Research Sources

Repository sources of truth:

- `.kiro/steering/product.md`
- `.kiro/steering/tech.md`
- `.kiro/steering/structure.md`
- `.kiro/steering/testing.md`
- `.kiro/steering/api-standards.md`
- `docs/extension-authoring.md`
- `internal/providerprofiles/{schema.go,compiler.go,embedded.go,certification.go,catalog.json}`
- `internal/standardplugins/{provider_profiles.go,provider_profile_binding.go,provider_profile_binding_test.go}`
- `internal/plugins/backends/modeldiscover/http_providers.go`
- archived `.kiro/specs/archive/extension-scalability-and-architecture-simplification/research.md`

Survey registries:

- Cline current generated provider catalog: `sdk/packages/llms/src/providers/providers.generated.ts`
- Cline current product overrides: `sdk/packages/llms/src/providers/builtins.ts`
- OpenCode provider docs and Models.dev-backed provider machinery
- Hermes Agent provider docs and `agent/models_dev.py`
- LiteLLM provider endpoint matrix, used only as breadth evidence
