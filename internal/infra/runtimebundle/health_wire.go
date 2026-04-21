package runtimebundle

import (
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/routinghealth"
)

func routingCandidateHealth(cfg *config.Config, now func() time.Time) policy.CandidateHealth {
	if cfg == nil {
		return routinghealth.Empty()
	}
	cb := cfg.Routing.Health.CircuitBreaker
	if !cb.Enabled {
		return routinghealth.Empty()
	}
	// Default when open_for omitted; invalid or non-positive durations are rejected by config.Validate before production Build.
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
