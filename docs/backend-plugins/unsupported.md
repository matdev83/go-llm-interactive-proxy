# Unsupported Backend Connectors

This document records backend services and subscription bridges that are explicitly **unsupported-by-policy** in Go-LIP due to the absence of public, third-party documented API contracts.

---

## `github-copilot` (GitHub Copilot Direct Bridge)

- **Status:** Unsupported-by-policy
- **Service Identity:** `https://api.githubcopilot.com`
- **Evaluation Date:** September 2026 (Task 8.2)

### Policy Rationale

GitHub Copilot does not publish or permit a third-party direct HTTP API for model inference. Direct access requires:
1. An internal, unpublished service-token exchange endpoint (`https://api.github.com/copilot_internal/v2/token`) that is not part of GitHub's public REST API catalog.
2. Hardcoded or spoofed editor client identifiers and private headers (`Editor-Version`, `Copilot-Integration-Id`, `Openai-Organization: github-copilot`).
3. Direct calls to `https://api.githubcopilot.com` which are restricted to first-party editor extensions.

Per Go-LIP architectural guardrails and provider ethics:
- **No private reverse-engineering:** Go-LIP does not integrate with unpublished `copilot_internal` endpoints or reverse-engineer editor-internal token flows.
- **No client spoofing:** Go-LIP transmits truthful User-Agent and client identifiers; spoofing VS Code or other first-party clients is prohibited.
- **No ACP substitution:** An ACP connector (`connectors/acp`, `cursorcliacp`) must not be substituted for direct HTTP model bridges.

### Verified Public Documentation Baseline

The following official GitHub documentation resources were reviewed to confirm the absence of any supported third-party model inference contract:

- **GitHub REST API for Copilot:**
  <https://docs.github.com/en/rest/copilot>
  *Scope:* Enterprise and organization usage metrics (`/copilot/usage`) and seat management (`/orgs/{org}/copilot/billing/seats`). No inference or model-entitlement endpoints are documented.

- **GitHub OAuth Device Flow:**
  <https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow>
  *Scope:* General authorization for GitHub user identities. Produces a standard GitHub user token (`gho_...`), but does not document or authorize Copilot model token exchange.

- **GitHub Copilot Extensions:**
  <https://docs.github.com/en/copilot/building-copilot-extensions>
  *Scope:* Webhook-based extensions that run within GitHub's UI or supported IDEs; does not provide standalone model inference endpoints.

- **GitHub Models:**
  <https://docs.github.com/en/github-models>
  *Scope:* Developer prototyping with GitHub personal access tokens against `https://models.inference.ai.azure.com` (Azure AI infrastructure), which is distinct from the GitHub Copilot subscription service.

If GitHub publishes an official, supported third-party direct inference API for Copilot in the future, this policy decision can be revisited.

---

## `claude-subscription` / `anthropic-oauth` (Claude Subscription / OAuth Bridge)

- **Status:** Unsupported-by-policy
- **Service Identity:** `https://api.anthropic.com`
- **Evaluation Date:** September 2026 (Task 8.4)

### Policy Rationale

Anthropic restricts consumer subscription OAuth credentials (Free, Pro, and Max plans, identifying tokens with prefix `sk-ant-oat01-`) exclusively to its official first-party user surfaces: `claude.ai` and the official `Claude Code` CLI tool.

Direct third-party access using subscription credentials requires:
1. Impersonating Claude Code's first-party client identity (reusing Claude Code's private OAuth client ID, spoofing `User-Agent`, `x-stainless-*`, and CLI headers).
2. Reverse-engineering internal OAuth/setup-token exchange endpoints not published as a public third-party API.
3. Violating Anthropic's Consumer Terms of Service, which prohibit automated or programmatic third-party access using consumer subscription accounts and enforce server-side rejection for non-Claude Code clients.

Per Go-LIP architectural guardrails and provider ethics:
- **No private reverse-engineering:** Go-LIP does not integrate with unpublished OAuth token exchange endpoints or extract tokens from local Claude Code profile caches.
- **No client spoofing:** Go-LIP transmits truthful `go-llm-interactive-proxy` User-Agent and client identification; spoofing Claude Code or Anthropic first-party applications is strictly prohibited.
- **Sanctioned API-key backend preserved:** Go-LIP provides full, first-class Anthropic model support through its in-process `anthropic` backend and `vertex` / `bedrock` connectors using officially sanctioned, commercially billed API keys and cloud provider credentials.

### Verified Public Documentation Baseline

The following official Anthropic documentation and legal resources were reviewed to confirm the policy restriction:

- **Anthropic Consumer Terms of Service:**
  <https://www.anthropic.com/legal/consumer-terms>
  *Scope:* Governs personal Free, Pro, and Max plans. Restricts subscription services to authorized personal consumer surfaces and prohibits automated, programmatic, or third-party extraction and routing.

- **Anthropic Commercial Terms of Service:**
  <https://www.anthropic.com/legal/commercial-terms>
  *Scope:* Governs commercial, production, and programmatic API access. Mandates the use of Anthropic Console API keys under commercial usage agreements.

- **Anthropic Claude Code Overview and Documentation:**
  <https://docs.anthropic.com/en/docs/agents-and-tools/claude-code/overview>
  *Scope:* Documents that profile-based OAuth login is built exclusively for the official Claude Code CLI terminal tool; external integrations, automated workflows, and third-party tools must supply a standard `ANTHROPIC_API_KEY`.

- **Anthropic API Authentication Getting Started:**
  <https://docs.anthropic.com/en/api/getting-started>
  *Scope:* Documents public model inference authentication via `x-api-key` header with `sk-ant-api03-...` keys. Anthropic does not offer or permit a public third-party OAuth 2.0 flow for consumer subscriptions to access the Messages API.

If Anthropic introduces an official, supported third-party OAuth 2.0 application model or developer subscription API in the future, this policy decision can be revisited.
