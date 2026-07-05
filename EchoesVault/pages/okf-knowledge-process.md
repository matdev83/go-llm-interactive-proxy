---
type: process
title: OKF Knowledge Process
description: Open Knowledge Format operating rules for EchoesVault consumers and maintainers.
stack: [markdown, yaml]
tags: [okf, knowledge-base, agents]
status: active
---

# OKF Knowledge Process

## Format Contract

EchoesVault uses an OKF-compatible directory of Markdown concept files under `EchoesVault/pages/` plus EchoesVault-specific daily logs under `EchoesVault/daily/`.

Required per concept page:
- YAML frontmatter at byte 0.
- `type` field in frontmatter (OKF required).
- One concept per file.
- Markdown body with factual, source-grounded content.

Recommended per concept page:
- `title` for display name.
- `description` for search/discovery summary.
- `tags` for query hints.
- `status` for lifecycle state.
- Normal Markdown links between concept files, for example `[routing](routing-orchestration.md)`.

## Reserved Files

- `EchoesVault/index.md`: directory listing for progressive disclosure. EchoesVault requires entries in `- [[filename]]: description` format.
- `EchoesVault/daily/YYYY-MM-DD.md`: chronological session log. Append-only scratchpad for completed work and decisions.

## Agent Access Pattern

1. Read `EchoesVault/index.md` first to discover available concepts.
2. Use `echoes_search_vault_pages` for targeted concept lookup before code or documentation changes.
3. Read the relevant page before updating it.
4. Prefer updating the existing concept file when the concept is unchanged; create a new file when a new durable concept exists.
5. Use `echoes_create_or_update_page` for concept writes so index updates stay synchronized.
6. Use `echoes_append_to_daily_log` after completing logical units of work or making durable architectural decisions.

## EchoesVault vs OKF Link Rule

OKF uses normal Markdown links for concept graph traversal. EchoesVault index entries use Obsidian-style `[[filename]]` because the project rule explicitly requires that format for the registry. Concept page bodies should prefer normal Markdown links.

## Source Priority

For this repository, source truth order is:
1. `.kiro/steering/` for durable project rules.
2. `AGENTS.md` and `.kiro/AGENTS.md` for agent workflow rules.
3. Source code and tests for implemented behavior.
4. `docs/` for operator and scenario documentation.
5. EchoesVault pages as compiled, agent-readable summaries.

EchoesVault pages must not invent behavior. If implementation and a page disagree, read code/steering, correct the page, and log the update.
