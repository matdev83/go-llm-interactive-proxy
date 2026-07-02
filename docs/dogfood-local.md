# Local dogfood workflow (no provider keys)

This is the canonical **alpha maintainer** path: validate configuration and inspect routes/extensions **without** API keys, using the **`local-stub`** backend and YAML under [`config/examples/`](../config/examples/).

Executable guarantees:

- Every `*.yaml` in `config/examples/` is exercised by bootstrap inspect in [`internal/infra/runtimebundle/example_configs_test.go`](../internal/infra/runtimebundle/example_configs_test.go) (`TestConfigExamples_passBootstrapInspect`).
- Cross-frontend smoke against the standard HTTP stack lives in [`internal/stdhttp/dogfood_smoke_test.go`](../internal/stdhttp/dogfood_smoke_test.go).

For proof-plugin seams and Python-era migration anchors, see [`docs/feature-migration-map.md`](feature-migration-map.md). For stage IDs and SDK surfaces, see [`docs/extension-points.md`](extension-points.md).

## Primary example

Use [`config/examples/dogfood-local-stub.yaml`](../config/examples/dogfood-local-stub.yaml) as the default **no-key** configuration (deterministic `local-stub` backend, all standard frontends enabled for validation).

Protocol-focused stub examples (same inspect coverage):

| File | Intent |
| --- | --- |
| `dogfood-local-stub.yaml` | General local stub; multi-frontend |
| `openai-responses-stub.yaml` | OpenAI Responses-shaped smoke |
| `openai-legacy-stub.yaml` | Legacy OpenAI chat smoke |
| `anthropic-stub.yaml` | Anthropic Messages smoke |
| `gemini-stub.yaml` | Gemini generateContent smoke |

## Commands (from repository root)

Substitute `CONFIG` with the example path you want.

**1. Validate configuration (no listener)**

```bash
go run ./cmd/lipstd check-config --config CONFIG
```

**2. Inspect effective routing (no listener)**

```bash
go run ./cmd/lipstd routes --config CONFIG
```

The JSON includes `effective_default_route`, `backends`, `model_aliases`, and **`credential_posture`**: `all_local_stub` when every **enabled** backend row uses factory kind `local-stub`, `live_provider` when any enabled backend is not `local-stub`, and `no_enabled_backends` when no backends are enabled. This distinguishes no-key stub operation from hosted backends **without** exposing API keys or other secrets (see requirement 6.3 in the stage-five spec).

**3. Inspect plugin and extension inventory (no listener)**

Requires registry context: pass the same `--config` path. Example:

```bash
go run ./cmd/lipstd inventory --config CONFIG
```

### Updating CLI JSON goldens (tests)

[`cmd/lipstd/golden_normalize_test.go`](../cmd/lipstd/golden_normalize_test.go) compares `lipstd routes` and `lipstd inventory` output to files under [`cmd/lipstd/testdata/dogfood-local-stub/`](../cmd/lipstd/testdata/dogfood-local-stub/) for [`config/examples/dogfood-local-stub.yaml`](../config/examples/dogfood-local-stub.yaml). If those tests fail after a deliberate change to route or inventory shape:

1. Re-run the same commands locally and capture JSON (or let the test print the diff).
2. Keep the **normalization contract** in `golden_normalize_test.go` in mind: list fields such as `backends` and `frontends` are sorted by `id` (and nested extension lists as documented there) so order flips do not spuriously fail.
3. Update the `*.golden.json` files to match the new **intended** operator-visible shape; do not relax assertions to hide accidental regressions.

**4. Serve**

Explicit:

```bash
go run ./cmd/lipstd serve --config CONFIG
```

Legacy equivalent (same behavior):

```bash
go run ./cmd/lipstd --config CONFIG
```

**5. Minimal request**

After serving with a stub example, exercise the mounted frontend URLs with your client of choice, or rely on the default-suite harness tests above for deterministic stubs.

**Note:** Stub examples often set `diagnostics.enabled: false`, so **`/healthz` is not mounted** until you enable diagnostics and set `diagnostics.health_path` (default `/healthz`). A quick TCP check is a `GET` on a mounted frontend path (for example **`GET /v1/responses`** returns **405** Method Not Allowed when the OpenAI Responses surface is enabled—proving the listener and mux are up).

## Diagnostics

Stub examples keep diagnostics disabled or loopback-safe where possible. If you enable diagnostics on a non-loopback listener, use [`README.md`](../README.md) guidance: bind safely and/or set `diagnostics.shared_secret` and header `X-LIP-Diagnostics-Secret`.

## Optional live-provider workflows

Hosted provider setups belong in [`config/config.yaml`](../config/config.yaml) and env-driven keys (`OPENAI_API_KEY`, etc.). Treat any live smoke as **optional** and **environment-gated**; do not assume CI or maintainers have credentials.

### vLLM CPU smoke (WSL, no GPU keys)

Proven maintainer path for exercising the **vLLM** OpenAI-compatible backend without hosted API keys: run a tiny CPU model in **WSL**, then drive [`scripts/vllm-text-smoke.ps1`](../scripts/vllm-text-smoke.ps1) from Windows PowerShell. The script defaults to `http://localhost:8000/v1`; pass **`-VllmBaseUrl`** when vLLM listens elsewhere. The script requires a non-empty proxy response by default; use `-ExpectedResponsePattern` only with models that reliably follow the exact smoke prompt.

**1. Start vLLM in WSL** (example venv `~/venvs/vllm-cpu`; model `facebook/opt-125m`; port **18000**):

```bash
python3 -m venv ~/venvs/vllm-cpu
source ~/venvs/vllm-cpu/bin/activate
pip install --upgrade pip
pip install uv
uv pip install vllm \
  --extra-index-url https://wheels.vllm.ai/nightly/cpu \
  --index-strategy first-index \
  --torch-backend cpu

cat > /tmp/vllm-chat-template.jinja <<'EOF'
{% for message in messages %}{% if message['role'] == 'user' %}User: {{ message['content'] }}
{% elif message['role'] == 'assistant' %}Assistant: {{ message['content'] }}
{% elif message['role'] == 'system' %}System: {{ message['content'] }}
{% endif %}{% endfor %}Assistant:
EOF

vllm serve facebook/opt-125m \
  --host 0.0.0.0 \
  --port 18000 \
  --api-key vllm \
  --dtype float \
  --max-model-len 128 \
  --served-model-name opt-125m \
  --chat-template /tmp/vllm-chat-template.jinja
```

The explicit chat template is required for **`/v1/chat/completions`** smoke (including the script's direct preflight and proxy path). Without it, chat requests against base models such as `opt-125m` typically fail with a missing chat-template error.

**2. Run the smoke script from the repository root (Windows PowerShell):**

```powershell
.\scripts\vllm-text-smoke.ps1 `
  -VllmBaseUrl http://127.0.0.1:18000/v1 `
  -VllmApiKey vllm `
  -Model opt-125m
```

WSL2 forwards `127.0.0.1:18000` on Windows to the listener in WSL. On success the script prints `vllm-text-smoke: PASS` and removes its temp config/logs directory; on failure it leaves logs under `%TEMP%\lip-vllm-smoke-*` for inspection.

## Maintainer integration gate (spec task 5.2)

After doc or wiring changes, run repository quality checks and the stage-focused test list from `.kiro/specs/archive/go-stage-five-dogfood-alpha-extension-proof/tasks.md` task **5.2**:

```bash
make quality-checks
go test -parallel=8 ./cmd/lipstd ./internal/core/config/... ./internal/core/diag/... ./internal/plugins/backends/localstub/... ./internal/plugins/features/... ./internal/stdhttp/... ./internal/core/runtime ./internal/pluginreg/... ./internal/archtest/...
```

**Environment notes:** `make test-race` is skipped on Windows (see `Makefile`); CI runs strict race on Linux. Optional Postgres-backed tests skip unless `LIP_TEST_POSTGRES_DSN` / `LIP_MANAGED_POSTGRES_DSN` are set (see CI workflow). These are normal skips, not failures.
