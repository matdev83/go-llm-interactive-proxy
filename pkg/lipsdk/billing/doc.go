// Package billing defines the minimal typed external monetary host binding.
//
// An external billing integration supplies one Binding: a stable identity and
// version plus typed ports for the cheap pre-route credit screen, the bounded
// quote and atomic exposure admission, the terminal evidence handoff with a
// durable acknowledgement, and explicit owned-resource lifecycle registration.
// Adapters translate these public DTOs to the existing internal billing
// services; the binding itself carries no SQL handles, executors, request
// bodies, or provider-shaped payloads.
//
// The provider-neutral observation/sideband DTOs and the Rater, Quoter,
// StatementImporter, and ReconciliationReader contracts remain defined in
// pkg/lipsdk/metering and pkg/lipsdk/economics. This package reuses them and
// introduces no generic provider-shaped normalizer port and no service-map or
// any-typed payload.
//
// Import DAG: billing -> economics -> metering, billing -> scope (no cycles).
//
// Boundary rules:
//   - Must not import internal/*, database/sql, net/http, Bun/driver types,
//     or provider SDKs and concrete plugins.
//   - DTOs are immutable-style public values using only public packages/types.
//   - No any, no generic maps standing in for typed APIs.
package billing
