---
type: architecture
title: Canonical Contracts
description: Protocol-neutral lipapi contracts and lipsdk registration/facade contracts.
stack: [go]
tags: [lipapi, lipsdk, contracts, canonical]
status: active
---

# Canonical Contracts

## `pkg/lipapi/` - Canonical Request & Event Model

The protocol-neutral middle. Every frontend adapter decodes to `lipapi` types; every backend adapter emits `lipapi` events.

### Key Types

| Type | Purpose |
|---|---|
| `Call` | Canonical request: messages, tools, model, params, capabilities |
| `Event` | Canonical stream event: content delta, tool calls, errors, completion |
| `EventStream` | Ordered sequence of `Event` values |
| `CapabilitySet` / `BackendCaps` / `NegotiationResult` | Capability declaration and negotiation contracts |
| `InvocationIdentity` / lineage fields | A-leg/B-leg and request identity metadata |
| `TokenAccounting` | Usage tracking |

### Design Rules

- Protocol-neutral; no provider SDK types
- Small and versionable
- Shaped around shared product semantics, not one-protocol feature parity
- Frontends map canonical/internal error categories to protocol-legal wire shapes

## `pkg/lipsdk/` - Plugin SDK

Stable contracts for plugin authors. No core implementation details.

### Key Contracts

| Contract | Purpose |
|---|---|
| `Registration` / `Requirement` | Plugin inventory entries and mandatory bundle validation |
| `FrontendMount` / `FrontendMountOptions` | Frontend route mounting contract |
| `BackendFactory` | Opaque SDK-level backend factory shape; standard distribution narrows it in `internal/pluginreg` |
| `internal/pluginreg.BackendFactory` | Standard distribution backend factory returning `execbackend.Backend` |
| `internal/pluginreg.FeatureFactory` | Standard distribution feature factory returning `feature.FeatureBundle` |
| `feature.FeatureBundle` | Versioned feature hook/session/workspace surface bundle |
| `hooks.*` | Hook SDK contracts |
| `standard_bundle.go` | Mandatory distribution plugin IDs |

### Facade Categories

- Request shaping and pre-request decisions: `request/`, `prerequest/`, `policydecision/`
- Session and transport auth: `session/`, `auth/`, `transport/`
- Workspace and scope: `workspace/`, `scope/`
- Tools and completion: `toolcatalog/`, `toolpolicy/`, `completion/`
- Traffic observation and state: `traffic/`, `state/`
- Continuity and auxiliary calls: `continuity/`, `auxiliary/`
- Usage and model inventory: `usage/`, `modelinventory/`
