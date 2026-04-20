// Package routing parses route selector strings and expands them into ordered attempt candidates.
//
// Grammar (v1): model-only, backend:model, backend.instance:model, failover (a|b|c),
// weighted ([weight=N]a^[weight=M]b), [first] on weighted branches, and ?query parameters.
// The planner applies exclusions, optional health filtering, session first-request rules,
// and deterministic weighted selection via an injected RNG.
package routing
