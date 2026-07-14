// Package metering defines provider-neutral public contracts for dual-plane
// metering facts, quantities, and journal append/query ports.
//
// These types are storage- and transport-agnostic. Implementations live in
// internal packages; enterprise and OSS adapters may depend on this package
// without importing internal/core.
//
// Boundary rules:
//   - Must not import internal/*, database/sql, net/http, or provider SDKs.
//   - May import pkg/lipsdk/scope for safe principal attribution on facts.
package metering
