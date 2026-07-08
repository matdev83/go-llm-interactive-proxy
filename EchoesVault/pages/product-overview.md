---
type: architecture
title: LLM Interactive Proxy Product Overview
description: Product identity, core promise, capability pillars, standard distribution, and Python LIP relationship.
stack: [go]
tags: [product, lip, control-plane]
status: active
---

# LLM Interactive Proxy (LIP) - Product Overview

## Identity

Universal translation, routing, and control plane for AI clients. Sits between AI clients and provider backends. Not a generic API gateway, not just a translation shim.

## Core Promise

- **Keep clients stable:** one endpoint instead of rewriting per-provider.
- **Stay flexible:** route across hosted providers, local runtimes, compatible APIs, agent-specific backends.
- **Fail clearly:** capability mismatches and required semantic loss must fail explicitly.
- **Recover pre-output:** swallow recoverable pre-output backend failures; surface post-output failures.
- **Preserve evidence:** routing, attempts, recovery, auth decisions, outcomes are observable and testable.
- **Stay maintainable:** small core, boring, hard to accidentally break.

## Capability Pillars

1. **Multi-frontend compatibility** - OpenAI Responses, legacy OpenAI chat, Anthropic Messages, Gemini generateContent.
2. **Multi-backend orchestration** - hosted/provider, local/compatible, agent-specific, and custom-compatible backend adapters.
3. **Canonical-in-the-middle translation** - protocols bridge through one canonical request model and event stream.
4. **Streaming-first execution** - streaming is default; non-streaming collects the same canonical stream.
5. **Core-owned routing & continuity** - failover, weighted routing, parallel races, TTFT budgets, B2BUA recovery.
6. **Extensibility without core coupling** - feature plugins consume `pkg/lipsdk` facades.
7. **Secure operator trust boundaries** - startup fails closed for auth, diagnostics, credential posture.

## Standard Distribution (`cmd/lipstd`)

Serves bundled HTTP frontends, routes through canonical [lipapi](canonical-contracts.md) requests/events, wires official backends and feature plugins via explicit registration (`internal/standardplugins/` tables installed onto an `internal/pluginreg/` registry).

## Relationship to Python LIP

Go implementation: smaller core, explicit plugin/SDK boundaries, runnable `cmd/lipstd`. Python sibling is historical context and migration reference only.
