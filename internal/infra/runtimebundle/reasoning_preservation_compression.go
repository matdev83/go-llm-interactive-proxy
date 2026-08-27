package runtimebundle

import (
	"fmt"
	"reflect"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactioncompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

type reasoningCompressionBinding struct {
	cfg       reasoningpreservation.Config
	policy    reasoningpreservation.EgressPolicy
	resolver  sdk.MatcherResolver
	sanitizer reasoningpreservation.TrustedTextSanitizer
}

func decodedReasoningCompressionBindings(ps *ProcessServices, regs []lipsdk.Registration, client auxiliary.BackgroundClient, poller auxiliary.BackgroundPoller) ([]reasoningCompressionBinding, error) {
	var out []reasoningCompressionBinding
	for _, reg := range regs {
		if reg.Kind != lipsdk.PluginKindFeature || !reg.Enabled || reg.RegistryFactoryKey() != reasoningpreservation.ID {
			continue
		}
		cfg, err := reasoningpreservation.DecodeConfig(reg.Config.Node)
		if err != nil {
			return nil, fmt.Errorf("reasoningpreservation: config: %w", err)
		}
		if !cfg.Compression.Enabled {
			continue
		}
		if isNilReasoningCapability(client) || isNilReasoningCapability(poller) {
			return nil, fmt.Errorf("reasoningpreservation: compression enabled requires BackgroundAux")
		}
		policy, ok := lookupReasoningEgressPolicy(ps, cfg.Compression.EgressPolicyRef)
		if !ok || isNilReasoningCapability(policy) {
			return nil, fmt.Errorf("reasoningpreservation: compression enabled requires trusted EgressPolicy for %q", cfg.Compression.EgressPolicyRef)
		}
		resolver := lookupReasoningMatcherResolver(ps)
		if isNilReasoningCapability(resolver) {
			return nil, fmt.Errorf("reasoningpreservation: compression enabled requires SecretGuard MatcherResolver")
		}
		sanitizer := reasoningpreservation.NewResolverSanitizer(resolver)
		if isNilReasoningCapability(sanitizer) {
			return nil, fmt.Errorf("reasoningpreservation: compression enabled requires BackgroundClient, BackgroundPoller, EgressPolicy, TrustedTextSanitizer")
		}
		out = append(out, reasoningCompressionBinding{cfg: cfg, policy: policy, resolver: resolver, sanitizer: sanitizer})
	}
	return out, nil
}

func validateReasoningPreservationCompressionGeneration(ps *ProcessServices, regs []lipsdk.Registration, client auxiliary.BackgroundClient, poller auxiliary.BackgroundPoller) error {
	_, err := decodedReasoningCompressionBindings(ps, regs, client, poller)
	return err
}

func bindReasoningPreservationCompression(merged featurebundle.MergedFeatureSurface, genMerged featurebundle.GeneratedMergeSurface, ps *ProcessServices, regs []lipsdk.Registration, client auxiliary.BackgroundClient, poller auxiliary.BackgroundPoller) (featurebundle.MergedFeatureSurface, featurebundle.GeneratedMergeSurface, error) {
	bindings, err := decodedReasoningCompressionBindings(ps, regs, client, poller)
	if err != nil {
		return featurebundle.MergedFeatureSurface{}, featurebundle.GeneratedMergeSurface{}, err
	}
	if len(bindings) == 0 {
		return merged, genMerged, nil
	}
	for _, b := range bindings {
		svc := reasoningpreservation.CompressionServices{
			Client:       client,
			Poller:       poller,
			EgressPolicy: b.policy,
			Sanitizer:    b.sanitizer,
		}
		_, bundle, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(b.cfg, svc, standardplugins.CodexCompanionPolicy())
		if err != nil {
			return featurebundle.MergedFeatureSurface{}, featurebundle.GeneratedMergeSurface{}, fmt.Errorf("reasoningpreservation: compression composition: %w", err)
		}
		if len(bundle.AttemptTransforms) > 0 {
			genMerged, err = genMerged.BindAttemptTransforms(reasoningpreservation.ID, bundle.AttemptTransforms)
			if err != nil {
				return featurebundle.MergedFeatureSurface{}, featurebundle.GeneratedMergeSurface{}, err
			}
		}
		if len(bundle.StreamObserverFactories) > 0 {
			genMerged, err = genMerged.BindStreamObserverFactories(reasoningpreservation.ID, bundle.StreamObserverFactories)
			if err != nil {
				return featurebundle.MergedFeatureSurface{}, featurebundle.GeneratedMergeSurface{}, err
			}
		}
	}
	merged.AttemptTransforms = lipfeature.Get(genMerged.Frozen, lipfeature.PlaneAttemptTransforms)
	return merged, genMerged, nil
}

func lookupReasoningEgressPolicy(ps *ProcessServices, ref string) (reasoningpreservation.EgressPolicy, bool) {
	if ps == nil || ps.opts == nil {
		return nil, false
	}
	if m := ps.opts.Production.ReasoningCompression.EgressPolicies; m != nil {
		if p, ok := m[ref]; ok {
			return p, true
		}
	}
	if m := ps.opts.Testing.ReasoningCompression.EgressPolicies; m != nil {
		if p, ok := m[ref]; ok {
			return p, true
		}
	}
	return nil, false
}

func lookupReasoningMatcherResolver(ps *ProcessServices) sdk.MatcherResolver {
	if ps == nil || ps.opts == nil {
		return nil
	}
	if r := ps.opts.Production.ReasoningCompression.MatcherResolver; !isNilReasoningCapability(r) {
		return r
	}
	if r := ps.opts.Testing.ReasoningCompression.MatcherResolver; !isNilReasoningCapability(r) {
		return r
	}
	return nil
}

func isNilReasoningCapability(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

func newReasoningCompressionGenerationRunner(ps *ProcessServices) (*compactioncompose.GenerationExecutorRunner, auxiliary.BackgroundClient, auxiliary.BackgroundPoller, error) {
	genRunner := compactioncompose.NewGenerationExecutorRunner()
	if isNilReasoningCapability(ps.BackgroundAux) {
		return genRunner, nil, nil, nil
	}
	boundClient := ps.BackgroundAux.BindRunner(genRunner)
	poller, ok := boundClient.(auxiliary.BackgroundPoller)
	if !ok {
		return nil, nil, nil, fmt.Errorf("runtimebundle: background scheduler bound client does not implement poller")
	}
	return genRunner, boundClient, poller, nil
}
