package routinghealth

import (
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
)

// CandidateHealthFromConfig returns a [policy.CandidateHealth] for the executor from cfg.
// When routing.health.circuit_breaker is disabled, returns [Empty]. When enabled, returns
// a [policy.CircuitBreaker] (failure threshold and open duration from config; invalid open_for
// defaults as documented on [config.CircuitBreakerConfig] validation).
func CandidateHealthFromConfig(cfg *config.Config, now func() time.Time) policy.CandidateHealth {
	if cfg == nil {
		return Empty()
	}
	cb := cfg.Routing.Health.CircuitBreaker
	if !cb.Enabled {
		return Empty()
	}
	// Default when open_for omitted; invalid or non-positive durations are rejected by
	// config.Validate before production Build.
	openFor := 30 * time.Second
	if s := strings.TrimSpace(cb.OpenFor); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			openFor = d
		}
	}
	return policy.NewCircuitBreaker(policy.CircuitBreakerOptions{
		FailureThreshold: cb.FailureThreshold,
		OpenDuration:     openFor,
		Now:              now,
	})
}
