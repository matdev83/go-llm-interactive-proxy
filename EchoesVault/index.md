# EchoesVault Index

Welcome to the EchoesVault knowledge base.

This index tracks all structured pages in the vault.

- [[product-overview]]: Product identity, core promise, and capability pillars of the LLM Interactive Proxy.
- [[architecture-overview]]: Hexagonal architecture, core ownership rules, and key architectural decisions.
- [[package-map]]: Complete package map with responsibilities across all five architectural zones.
- [[canonical-contracts]]: Canonical request/event model (`pkg/lipapi`) and plugin SDK (`pkg/lipsdk`) contracts.
- [[routing-orchestration]]: Route planning, selectors, failover, parallel races, TTFT budgets, and B2BUA recovery.
- [[plugin-system]]: Frontend, backend, and feature plugin architecture with explicit registration model.
- [[streaming-model]]: Streaming-first execution, canonical event stream, and stream component design.
- [[continuity-recovery]]: B2BUA continuity semantics, lineage model, and continuity stores.
- [[testing-strategy]]: Testing philosophy, suite topology, build tags, and high-value test targets.
- [[controlplane-evidence-four-layer-pattern]]: Audit property requiring every control-plane evidence guard to be regression-locked at SDK, core, normalizer, and recorder layers — see docs/controlplane-evidence.md for the full coverage map.
- [[security-auth]]: Startup posture, transport auth, secure sessions, and credential management.
- [[proxy-identity]]: A-leg Server and B-leg User-Agent / OpenRouter attribution carriers, modes, allowlist, and exclusions.
- [[tech-stack]]: Go runtime, dependencies, tooling, and structural patterns.
- [[agent-skills]]: Skill-to-task mapping for golang-specific agent skill loading, hexagonal architecture enforcement, and registered skill inventory.
- [[okf-knowledge-process]]: OKF-compatible operating rules for EchoesVault concept files, discovery, linking, and agent access.
- [[codex-app-server-backend]]: OpenAI Codex CLI app-server backend — protocol mapping, handshake, lifecycle, and event translation for the local-agent stdio backend.
- [[cursor-sdk-backend]]: Experimental Cursor SDK backend — operator install, auth/billing separation, local-only routing, safety defaults, and process-local continuity.
- [[postgres-transaction-pooling]]: PostgreSQL admin/runtime separation, shared pool ownership, schema lifecycle, and pooled release gates.
- [[tool-call-repair]]: Canonical native tool-call repair (ADR 0007): finalizer seam, deterministic V1 matrix, buffering, and config defaults.
- [[decode-qos-admission]]: Shared frontend decode admission limiter: finite defaults, 413 vs 429, handler order, inventory numbers.
