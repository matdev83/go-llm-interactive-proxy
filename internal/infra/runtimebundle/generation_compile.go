package runtimebundle

import (
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/runtimegen"
)

// CompileExecutableGeneration builds a validated executable generation from
// production registrations and static concurrency limits (tasks 5.2–5.3).
func CompileExecutableGeneration(cfg *config.Config, prod ProductionOptions, now time.Time) (*snapshotgen.ExecutableGeneration, error) {
	contrib := CompileGenerationContribution(cfg, prod, now)
	return snapshotgen.CompileExecutable(contrib)
}

// CompileGenerationContribution maps static config + production registrations
// into the public GenerationContribution shape (requirement 11.6; design D15).
func CompileGenerationContribution(cfg *config.Config, prod ProductionOptions, now time.Time) runtimegen.GenerationContribution {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	version := "static"
	if cfg != nil {
		if v := strings.TrimSpace(cfg.Accounting.Concurrency.SnapshotVersion); v != "" {
			version = v
		} else if v := strings.TrimSpace(cfg.Accounting.Authority.SnapshotVersion); v != "" {
			version = v
		}
	}
	contrib := runtimegen.GenerationContribution{
		SourceID:                "static-config",
		Version:                 version,
		EffectiveAt:             now,
		State:                   economics.SnapshotReady,
		RequestRegistrations:    append([]authority.RequestRegistration(nil), prod.RequestRegistrations...),
		AttemptRegistrations:    append([]authority.AttemptRegistration(nil), prod.AttemptRegistrations...),
		ConcurrencyRegistration: prod.ConcurrencyRegistration,
		MaxActiveRequests:       maxActiveFromConfig(cfg),
	}
	for _, reg := range prod.RaterRegistrations {
		switch reg.Perspective {
		case metering.PerspectiveCustomer:
			contrib.CustomerRaters = append(contrib.CustomerRaters, reg)
		case metering.PerspectiveOperator:
			contrib.OperatorRaters = append(contrib.OperatorRaters, reg)
		}
	}
	return contrib
}

func maxActiveFromConfig(cfg *config.Config) int {
	if cfg == nil || !cfg.Accounting.Concurrency.Enabled {
		return 0
	}
	max := 0
	for _, rule := range cfg.Accounting.Concurrency.Rules {
		if rule.MaxActiveRequests > max {
			max = rule.MaxActiveRequests
		}
	}
	return max
}

// PublishExecutableFromProduction compiles and publishes when contribution is non-empty.
// Failed compile/publish leaves the prior executable generation active.
func PublishExecutableFromProduction(pub *snapshotgen.Publisher, cfg *config.Config, prod ProductionOptions, now time.Time) (*snapshotgen.ExecutableGeneration, error) {
	if pub == nil {
		return nil, fmt.Errorf("runtimebundle: nil snapshot publisher")
	}
	contrib := CompileGenerationContribution(cfg, prod, now)
	if err := contrib.Validate(); err != nil {
		return pub.CurrentExecutable(), err
	}
	gen, err := snapshotgen.CompileExecutable(contrib)
	if err != nil {
		return pub.CurrentExecutable(), err
	}
	if prior := pub.Current(); prior != nil {
		// Copy source-fetch metadata planes for compatibility views only.
		// Executable State remains the contribution/enforcement posture (9.6, 12.1).
		gen.Usage = prior.Usage
		gen.Concurrency = prior.Concurrency
		gen.Rating = prior.Rating
	}
	return pub.PublishExecutable(gen)
}
