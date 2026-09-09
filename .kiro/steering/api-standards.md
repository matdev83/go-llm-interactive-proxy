# API Standards (Steering)

## Architectural Invariants

1. **Canonical in the middle** — frontends decode wire protocols to `pkg/lipapi`; backends translate canonical calls to provider protocols and canonicalize the resulting events.
2. **No pairwise translators** — protocol A must never be translated directly to protocol B as a product architecture.
3. **Streaming is primary** — non-streaming responses are collected from the same canonical event path.
4. **Wire legality is adapter-owned** — framing, status codes, headers, terminal errors, and transport-specific lifecycle rules remain legal for the active frontend/backend protocol.
5. **Capabilities are explicit** — required semantics are negotiated before upstream execution where possible; unsupported semantics fail rather than disappearing silently.
6. **Canonical contracts are provider-neutral** — a type belongs in `pkg/lipapi` only when it represents cross-protocol product semantics, not because one provider exposes a convenient field.
7. **Opaque semantics stay opaque** — provider-specific signed/structured/continuation artifacts may be carried canonically when preservation is required, but the proxy must not silently reinterpret one provider dialect as another.
8. **Errors cross boundaries deliberately** — internal/provider details are classified and mapped to protocol-legal client errors; stack traces, local paths, secrets, and raw sensitive payloads do not cross the A-leg boundary.

## Ownership Rules

### Frontend adapters

Frontends are driving adapters. They own:

- wire request decoding and validation;
- HTTP/SSE/WebSocket lifecycle and protocol framing;
- canonical-to-wire response/error encoding;
- transport-specific identity/session carriers;
- frontend-only compatibility behavior.

They do **not** own routing policy, backend selection, retry/failover semantics, or provider SDK calls.

### Backend adapters and connectors

Backends are driven adapters. They own:

- canonical-to-provider request translation;
- provider SDK/client use;
- provider transport quirks and authentication mechanics;
- provider response/error translation back to canonical events;
- provider-specific capability declaration.

Provider SDK types must stay inside backend/connector boundaries. Shared compatible-protocol helpers may be reused by a protocol family, but they must not become a second canonical layer.

### Canonical/core packages

Canonical and core packages may define only semantics required to coordinate providers/protocols generically. They must not import provider SDKs or concrete protocol/feature plugins.

## Capability and Dialect Policy

- Required client semantics must be represented in capability negotiation or fail explicitly.
- A downgrade is acceptable only when its lossiness is modeled and permitted by the negotiated contract.
- Provider-specific structured artifacts that must survive failover/continuation should use bounded, versionable opaque dialect carriers rather than polluting generic structs with provider fields.
- Cross-provider conversion of opaque dialects is forbidden unless an explicit, tested semantic conversion contract exists.
- Derived metadata used by policy must be conservative. Unknown inputs should bias toward safety rather than falsely claiming precision or permission.

## Identity, Session, and Authority

- Session authority is proxy-owned; client-provided session identifiers or hints are untrusted inputs.
- Product identity on the client leg and provider identity on backend legs are separate concerns.
- Authentication/authorization decisions must be made before backend execution and must not be inferred from protocol convenience fields.
- Derived classification metadata is not authorization by itself.

## Protocol Change Procedure

When adding or changing a protocol surface:

1. **Classify the change**: wire-only, canonical semantic, or shared execution semantic.
2. **Keep the smallest ownership surface**:
   - wire-only → frontend/backend adapter;
   - cross-protocol data semantic → `pkg/lipapi`;
   - provider-neutral orchestration semantic → core/SDK seam.
3. **Define capability behavior** before implementation: supported, explicitly degraded, or rejected.
4. **Preserve the streaming path**; do not add a separate non-streaming executor.
5. **Add contract/conformance coverage** at the family boundary. Avoid frontend×backend Cartesian tests unless a unique cross-boundary invariant truly requires one.
6. **Verify error and cancellation legality** for the affected wire protocol.
7. **Keep provider-specific dependencies at the edge** and run architecture guards.

If a new provider or protocol implementation follows these rules, its addition does not require updating this steering file.

## Current Surface Lookup

Do not maintain supported-provider/protocol inventories here. Current state is derived from:

- standard contributions under `internal/standardplugins/`;
- canonical/public contracts under `pkg/lipapi` and `pkg/lipsdk`;
- provider-profile catalogs under `internal/providerprofiles/`;
- executable connector manifests under `connectors/`;
- protocol/operator documentation and contract TCKs under `docs/` and `internal/testkit/`.
