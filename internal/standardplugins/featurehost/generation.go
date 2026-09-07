package featurehost

import (
	"context"
	"fmt"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/reasoningcompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/secretguardcompose"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

// CompileGeneration compiles standard feature planes, lifecycles, and core ports
// for a candidate generation. It does NOT construct any process-scoped resource
// listed in the Task 1.1 transition table (Requirement 8.3, Task 2.2).
// Candidate failure must not mutate process feature state.
func (r *Runtime) CompileGeneration(ctx context.Context, in GenerationInput) (GenerationOutput, error) {
	if in.FaultInject != nil {
		return GenerationOutput{}, in.FaultInject
	}

	// When r is nil (StandardFeatures disabled), return disabled feature output.
	if r == nil {
		if err := r.validateCompactionPrerequisites(in.Registrations); err != nil {
			return GenerationOutput{}, err
		}
		bundle := lipfeature.FeatureBundle{
			PlaneSet:   in.Planes,
			Lifecycles: slices.Clone(in.Lifecycles),
		}
		return GenerationOutput{
			Bundle:     bundle,
			Planes:     in.Planes,
			Lifecycles: slices.Clone(in.Lifecycles),
			CorePorts:  CorePorts{},
		}, nil
	}

	// 1. Compaction continuity prerequisites validation.
	if err := r.validateCompactionPrerequisites(in.Registrations); err != nil {
		return GenerationOutput{}, err
	}

	surface := in.MergeSurface
	if surface.Frozen.IsZero() && !in.Planes.IsZero() {
		surface = featurebundle.GeneratedMergeSurface{
			Frozen:     in.Planes,
			Lifecycles: slices.Clone(in.Lifecycles),
		}
	}

	// 2. Compaction continuity surface binding.
	var err error
	surface, err = r.bindCompactionContinuity(surface, in.Registrations)
	if err != nil {
		return GenerationOutput{}, err
	}

	// 3. Reasoning composition. The facade merges production/testing options
	// internally so callers never interpret reasoning policy (Task 2.4).
	reasoningOpts := composeReasoningOptions(in.ReasoningProdOpts, in.ReasoningTestOpts)
	if err := reasoningcompose.Validate(reasoningcompose.GenerationInput{
		Registrations: in.Registrations,
		Client:        in.BackgroundClient,
		Poller:        in.BackgroundPoller,
		Options:       reasoningOpts,
	}); err != nil {
		return GenerationOutput{}, fmt.Errorf("featurehost: reasoning validation: %w", err)
	}

	staged, err := reasoningcompose.Bind(surface, reasoningcompose.GenerationInput{
		Registrations: in.Registrations,
		Client:        in.BackgroundClient,
		Poller:        in.BackgroundPoller,
		Options:       reasoningOpts,
	})
	if err != nil {
		return GenerationOutput{}, fmt.Errorf("featurehost: reasoning composition: %w", err)
	}

	outPlanes := staged.Frozen
	if outPlanes.IsZero() {
		outPlanes = in.Planes
	}
	outLifecycles := slices.Clone(staged.Lifecycles)
	if len(outLifecycles) == 0 && len(in.Lifecycles) > 0 {
		outLifecycles = slices.Clone(in.Lifecycles)
	}

	// 4. Secret Guard composition
	guards := lipfeature.Get[[]sdk.Guard](outPlanes, lipfeature.PlaneSecretGuards)
	sgOut, err := secretguardcompose.Compose(secretguardcompose.Input{
		AccessMode:       in.AccessMode,
		Registrations:    in.Registrations,
		Guards:           guards,
		Environment:      in.SecretEnv,
		Inputs:           in.SecretInputs,
		DecisionObserver: in.DecisionObserver,
		Logger:           r.logger,
	})
	if err != nil {
		return GenerationOutput{}, fmt.Errorf("featurehost: secret guard composition: %w", err)
	}

	out := GenerationOutput{
		Bundle: lipfeature.FeatureBundle{
			PlaneSet:   outPlanes,
			Lifecycles: slices.Clone(outLifecycles),
		},
		Planes:               outPlanes,
		Lifecycles:           outLifecycles,
		SecretGuard:          sgOut.Plane,
		SecretGuardInventory: sgOut.Inventory,
		CorePorts: CorePorts{
			CompactionDetector: r.compactionDetector,
		},
	}

	return out, nil
}
