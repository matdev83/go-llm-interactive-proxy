# ADR 0004: Candidate-aware capability resolution

## Status

Accepted (stage two).

## Context

Backend capability varies by model and route flavor. Static per-backend caps are insufficient for deterministic negotiation before upstream I/O.

## Decision

- Introduce `internal/core/capabilities.Resolver` (or equivalent) used by the executor for each `(backend id, AttemptCandidate, attempt call)` tuple.
- Bundled backends supply descriptor tables or small catalog functions; the core never imports vendor SDKs.
- Required-capability mismatches **reject** before `Open`; negotiated downgrades are explicit and logged.

## Consequences

- Catalog maintenance rules live with provider docs (`docs/capability-catalogs.md`); regression tests lock representative models.
