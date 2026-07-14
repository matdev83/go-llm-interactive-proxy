// Package authority defines public request- and attempt-level authority
// provider contracts, decisions, safe evidence, and concurrency lease DTOs.
//
// Import DAG: authority → economics → metering (no cycles).
// Implementations and stores live outside this package (Phase 6+ / 8).
//
// Boundary rules:
//   - Must not import internal/*, database/sql, net/http, or provider SDKs.
//   - Must not reference Executor or runtimebundle types.
package authority
