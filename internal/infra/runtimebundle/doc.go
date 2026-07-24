// Package runtimebundle is the standard-distribution composition root: assembles continuity
// store, executor (production clock/RNG, routing health, route observation), shared upstream HTTP,
// and resource shutdown hooks. [BuildHost] owns serve/public Build; [InspectRoutes]/
// [InspectInventory] own operator inspection; [BuildBootstrap] remains check-config-only until
// Task 5.4. [NewBootstrapApp] remains for callers that need a standalone [coreruntime.App].
// Executor routing health is built with
// [github.com/matdev83/go-llm-interactive-proxy/internal/infra/routinghealth.CandidateHealthFromConfig].
//
// Upstream HTTP: backends receive the client from [Build] ([BuildOptions.Infra.HTTPClient];
// default from httpclient.TransportTuneFromConfig plus StandardWithTrustEnvironment / StandardWithTune).
//
// Routing health: when routing.health.circuit_breaker.enabled, CandidateHealth is a core policy
// circuit breaker; otherwise a no-op. Route observation: non-nil logger → info "lip.route"; nil → noop.
//
// Stage-three decisions (locked):
//   - Plugin identity: YAML "kind" is factory id; "id" is instance id (legacy: "id" serves as both).
//   - Continuity: ttl/max_legs apply to in-memory store only; SQLite rejects those until pruning exists.
//   - This package lives outside internal/core/runtime to keep core orchestration-only.

package runtimebundle
