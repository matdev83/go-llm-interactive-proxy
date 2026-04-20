# Architecture Bootstrap Notes

This repository bootstrap follows the boundaries captured in `AGENTS.md` and the active
Kiro spec `go-core-reimplementation-v1`.

Current intent:

- keep the core provider-agnostic
- keep protocol adapters under `internal/plugins/`
- keep canonical public contracts in `pkg/lipapi`
- keep plugin contracts in `pkg/lipsdk`
- keep runtime behavior unimplemented until future spec tasks land

This document is intentionally short because the source of truth for design remains:

- `.kiro/steering/*.md`
- `.kiro/specs/go-core-reimplementation-v1/design.md`
- `.kiro/specs/go-core-reimplementation-v1/tasks.md`
