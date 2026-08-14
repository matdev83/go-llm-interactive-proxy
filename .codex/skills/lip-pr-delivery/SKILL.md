---
name: lip-pr-delivery
description: "Deliver sequential LIP PRs: preflight, CI repair, merge order, smoke checks, and cleanup."
user-invocable: true
license: MIT
compatibility: Designed for pi, OpenCode, Codex, Cursor, and similar coding agents working in this repository.
metadata:
  author: go-llm-interactive-proxy
  version: "1.0.0"
allowed-tools: Read Edit Write Glob Grep Bash(git:*) Bash(gh:*) Bash(go:*) Bash(make:*) Agent AskUserQuestion
---

# LIP Sequential PR Delivery

This is an explicit Codex-compatible discovery copy of the repository skill.

Load and follow the canonical skill at:

`.cursor/skills/lip-pr-delivery/SKILL.md`

The canonical file is part of this repository and defines the complete mandatory workflow for worktree safety, independent tests, exact formatting and coverage preflight, sequential submission, CI failure reproduction and repair, clean-only merging, merged-main smoke verification, and Windows-aware cleanup.

Do not proceed with PR delivery until that complete file has been read. Repository `AGENTS.md` and `.kiro/steering/` override the skill.
