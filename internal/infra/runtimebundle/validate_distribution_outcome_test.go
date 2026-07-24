package runtimebundle

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	coresg "github.com/matdev83/go-llm-interactive-proxy/internal/core/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
)

// Test-facing ValidateDistribution outcome/journal types (Task 5.4 RED matrices).
// Mirrors hostBuildJournal/hostBuildOutcome (host_build_outcome_test.go) so the
// PartialCleanup matrix style stays identical across BuildHost and
// ValidateDistribution.

type validateDistributionJournal struct {
	Acquired []string
	Cleaned  []string
	Loads    int
}

// validateDistributionOutcome invokes the production validateDistribution
// transaction with a counting load wrapper and a journaling probe. It must
// not reimplement any ValidateDistribution stage.
func validateDistributionOutcome(ctx context.Context, in ValidateDistributionInput, loadEffective bootstrapEffectiveLoader) (validateDistributionJournal, error) {
	loads := 0
	wrapped := loadEffective
	if loadEffective != nil {
		inner := loadEffective
		wrapped = func(ctx context.Context, path string, cli config.StreamRecoveryOverrides) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, config.StreamRecoveryOverrides, error) {
			loads++
			return inner(ctx, path, cli)
		}
	}
	var journal validateDistributionJournal
	probe := func(stage validateDistributionStage, event validateDistributionProbeEvent) error {
		switch event {
		case validateProbeAcquired:
			journal.Acquired = append(journal.Acquired, string(stage))
		case validateProbeCleaned:
			journal.Cleaned = append(journal.Cleaned, string(stage))
		}
		return nil
	}
	err := validateDistribution(ctx, in, coresg.Environment(nil), wrapped, probe)
	journal.Loads = loads
	return journal, err
}
