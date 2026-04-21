// Package runtimebundle is the standard-distribution composition root: assembles continuity
// store, executor (production clock/RNG, routing health, route observation), shared upstream HTTP,
// and resource shutdown hooks.
//
// Upstream HTTP: backends that call providers over HTTP receive the client from [Build] (see
// [BuildOptions.HTTPClient]; default [github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient.Standard]).
//
// Routing health: when config sets routing.health.circuit_breaker.enabled, the executor's
// CandidateHealth is a core policy circuit breaker (failure_threshold, open_for duration string).
// When disabled, CandidateHealth is an empty no-op implementation.
//
// Route observation: when [Build] receives a non-nil logger, the executor's RouteObserver logs
// coarse routing decisions at info ("lip.route", trace_id, decision, detail). A nil logger uses a
// noop observer so the field is always non-nil.
//
// Stage-three decisions (locked):
//   - Plugin identity: YAML field "kind" is factory/registry id; "id" is runtime instance id.
//     When "kind" is omitted, "id" serves as both (legacy configs).
//   - Continuity: ttl/max_legs apply to in-memory store only; SQLite rejects those fields until
//     pruning exists (see config validation).
//   - This package lives outside internal/core/runtime to keep core orchestration-only.

package runtimebundle
