package featurehost

import (
	"context"
	"fmt"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost/reasoning"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
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
	var genBound boundHostFeatures
	if len(in.HostRegistrations) > 0 {
		var err error
		genBound, err = bindHostRegistrations(in.HostRegistrations)
		if err != nil {
			return GenerationOutput{}, fmt.Errorf("featurehost: host registrations: %w", err)
		}
		reasoningOpts = composeReasoningOptions(reasoningOpts, genBound.reasoning)
	} else if len(r.boundReasoning.EgressPolicies) > 0 || r.boundReasoning.MatcherResolver != nil {
		reasoningOpts = composeReasoningOptions(reasoningOpts, r.boundReasoning)
	}
	if err := reasoning.Validate(reasoning.GenerationInput{
		Registrations: in.Registrations,
		Client:        in.BackgroundClient,
		Poller:        in.BackgroundPoller,
		Options:       reasoningOpts,
	}); err != nil {
		return GenerationOutput{}, fmt.Errorf("featurehost: reasoning validation: %w", err)
	}

	staged, err := reasoning.Bind(surface, reasoning.GenerationInput{
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

	// 4. Secret Guard composition. Bound host inputs are selected by binding
	// presence: a supplied secret-guard binding (generation-bound, else
	// process-bound) is preferred wholesale; legacy inputs apply only when no
	// binding is present. The host pointer lets composition overlay
	// explicitly-set host fields onto YAML-decoded options.
	guards := lipfeature.Get[[]sdk.Guard](outPlanes, lipfeature.PlaneSecretGuards)
	sgEnv := in.SecretEnv
	sgInputs := in.SecretInputs
	var sgHostInputs *SecretGuardInputs
	if len(in.HostRegistrations) > 0 {
		if genBound.secretGuard.Environment != nil {
			sgEnv = genBound.secretGuard.Environment
		}
		if genBound.secretGuard.Present {
			sgInputs = genBound.secretGuard.Inputs
			sgHostInputs = &sgInputs
		}
	} else if r != nil {
		if sgEnv == nil && r.boundSecretGuard.Environment != nil {
			sgEnv = r.boundSecretGuard.Environment
		}
		if r.boundSecretGuard.Present {
			sgInputs = r.boundSecretGuard.Inputs
			sgHostInputs = &sgInputs
		}
	}
	sgOut, err := buildSecretGuardRuntime(SecretGuardBuildInput{
		AccessMode:       in.AccessMode,
		Registrations:    in.Registrations,
		Guards:           guards,
		Environment:      sgEnv,
		Inputs:           sgInputs,
		HostInputs:       sgHostInputs,
		DecisionObserver: in.DecisionObserver,
		Logger:           r.logger,
	})
	if err != nil {
		return GenerationOutput{}, fmt.Errorf("featurehost: secret guard composition: %w", err)
	}

	// 5. Interleaved Thinking processor
	var interleavedProc runtime.InterleavedProcessor
	ic := in.InterleavedConfig
	var featureEntryFound bool
	for _, r := range in.Registrations {
		if r.Kind == lipsdk.PluginKindFeature && (r.ID == interleavedthinking.ID || r.FactoryKind == interleavedthinking.ID) {
			featureEntryFound = true
			decoded, err := interleavedthinking.DecodeConfig(r.Config.Node)
			if err != nil {
				return GenerationOutput{}, fmt.Errorf("featurehost: interleaved config: %w", err)
			}
			if !ic.Enabled && decoded.Enabled {
				ic = decoded
			}
			break
		}
	}

	// Conflict detection: if both canonical feature entry and legacy config.interleaved are enabled
	if in.ConfigInterleaved.Enabled && featureEntryFound && ic.Enabled {
		return GenerationOutput{}, fmt.Errorf("featurehost: both legacy config.interleaved and canonical feature %q are configured", interleavedthinking.ID)
	}

	// Fallback to legacy config.interleaved carrying the FULL mapped policy with defaults
	if !ic.Enabled && in.ConfigInterleaved.Enabled {
		ic = interleavedthinking.Config{
			Enabled:               true,
			StreamToClient:        interleavedthinking.DefaultStreamToClient,
			RegularTurnsRemaining: interleavedthinking.DefaultRegularTurns,
			MaxMemoBytes:          interleavedthinking.DefaultMaxMemoBytes,
		}
	}
	if ic.Enabled {
		if err := ic.Validate(); err != nil {
			return GenerationOutput{}, fmt.Errorf("featurehost: interleaved processor: %w", err)
		}
		if ic.InstructionsFile != "" && ic.Instructions == "" {
			instructions, err := interleavedthinking.ResolveInstructions(in.ConfigDir, ic.InstructionsFile, "")
			if err != nil {
				return GenerationOutput{}, fmt.Errorf("featurehost: interleaved processor: %w", err)
			}
			ic.Instructions = instructions
			ic.InstructionsFile = ""
		}
		store := interleavedthinking.NewMemoStore(ic.EffectiveMaxMemoBytes())
		proc, err := newInterleavedProcessor(ic, store)
		if err != nil {
			return GenerationOutput{}, fmt.Errorf("featurehost: interleaved processor: %w", err)
		}
		interleavedProc = NewInterleavedProcessorAdapter(proc)
	}

	// 6. Keep-warm prompt-cache maintenance (Task 6.2/6.3)
	kwMaint, kwMgr, kwQuiesce, err := r.compileKeepwarm(in)
	if err != nil {
		return GenerationOutput{}, err
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
		KeepwarmManager:      kwMgr,
		KeepwarmQuiesce:      kwQuiesce,
		CorePorts: CorePorts{
			CompactionDetector:     r.compactionDetector,
			ConversationReader:     r.ConversationReader(),
			InterleavedProcessor:   interleavedProc,
			PromptCacheMaintenance: kwMaint,
			TerminalPolicyReader:   r.TerminalPolicyReader(),
		},
	}

	return out, nil
}
