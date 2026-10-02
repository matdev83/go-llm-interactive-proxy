package secretguard

import (
	"fmt"

	blconfig "github.com/betterleaks/betterleaks/v2/config"
	blregexp "github.com/betterleaks/betterleaks/v2/regexp"
	blscan "github.com/betterleaks/betterleaks/v2/scan"
)

// betterLeaksScanner is the feature-private generation handle for BetterLeaks.
// The upstream scanner is immutable after construction and safe for concurrent
// scans; keeping it behind this handle prevents BetterLeaks types from becoming
// part of the feature, runtime, or SDK contracts.
type betterLeaksScanner struct {
	scanner *blscan.Scanner
}

// newBetterLeaksScanner constructs one precompiled scanner for a runtime
// generation. Disabled policy returns no handle so callers cannot accidentally
// create BetterLeaks work for a disabled detector.
func newBetterLeaksScanner(policy BetterLeaksPolicy) (*betterLeaksScanner, error) {
	if !policy.Enabled {
		return nil, nil
	}
	if err := validateBetterLeaksScannerPolicy(policy); err != nil {
		return nil, err
	}

	cfg, err := blconfig.Default()
	if err != nil {
		return nil, fmt.Errorf("secretguard: betterleaks default config: %w", err)
	}

	scanner, err := blscan.New(cfg,
		blscan.WithRegexEngine(blregexp.Stdlib{}),
		blscan.WithWorkers(policy.Workers),
		blscan.WithMinimumConfidence(policy.MinimumConfidence),
		blscan.WithMaxDecodeDepth(policy.MaxDecodeDepth),
		blscan.WithPrecompile(),
		blscan.WithAllowSignatures(),
	)
	if err != nil {
		return nil, fmt.Errorf("secretguard: construct betterleaks scanner: %w", err)
	}
	return &betterLeaksScanner{scanner: scanner}, nil
}

func validateBetterLeaksScannerPolicy(policy BetterLeaksPolicy) error {
	if policy.Workers < 1 || policy.Workers > MaxBetterLeaksWorkers {
		return fmt.Errorf("secretguard: betterleaks workers must be between 1 and %d", MaxBetterLeaksWorkers)
	}
	if policy.MaxDecodeDepth < 0 || policy.MaxDecodeDepth > MaxBetterLeaksDecodeDepth {
		return fmt.Errorf("secretguard: betterleaks max decode depth must be between 0 and %d", MaxBetterLeaksDecodeDepth)
	}
	switch policy.MinimumConfidence {
	case blscan.ConfidenceLow, blscan.ConfidenceMedium, blscan.ConfidenceHigh:
	default:
		return fmt.Errorf("secretguard: betterleaks minimum confidence must be low, medium, or high")
	}
	return nil
}
