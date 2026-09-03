package runtimebundle

import (
	"fmt"
	"reflect"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactioncompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/reasoningcompose"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

func validateReasoningPreservationCompressionGeneration(ps *ProcessServices, regs []lipsdk.Registration, client auxiliary.BackgroundClient, poller auxiliary.BackgroundPoller) error {
	opts := resolveReasoningCompressionOptions(ps)
	return reasoningcompose.Validate(reasoningcompose.GenerationInput{
		Registrations: regs,
		Client:        client,
		Poller:        poller,
		Options:       opts,
	})
}

func bindReasoningPreservationCompression(genMerged featurebundle.GeneratedMergeSurface, ps *ProcessServices, regs []lipsdk.Registration, client auxiliary.BackgroundClient, poller auxiliary.BackgroundPoller) (featurebundle.GeneratedMergeSurface, error) {
	opts := resolveReasoningCompressionOptions(ps)
	return reasoningcompose.Bind(genMerged, reasoningcompose.GenerationInput{
		Registrations: regs,
		Client:        client,
		Poller:        poller,
		Options:       opts,
	})
}

func resolveReasoningCompressionOptions(ps *ProcessServices) reasoningcompose.Options {
	if ps == nil || ps.opts == nil {
		return reasoningcompose.Options{}
	}
	return reasoningcompose.ComposeOptions(ps.opts.Production.ReasoningCompression, ps.opts.Testing.ReasoningCompression)
}

func lookupReasoningMatcherResolver(ps *ProcessServices) sdk.MatcherResolver {
	return resolveReasoningCompressionOptions(ps).MatcherResolver
}

func isNilReasoningCapability(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return rv.IsNil()
	default:
		return false
	}
}

func newReasoningCompressionGenerationRunner(ps *ProcessServices) (*compactioncompose.GenerationExecutorRunner, auxiliary.BackgroundClient, auxiliary.BackgroundPoller, error) {
	genRunner := compactioncompose.NewGenerationExecutorRunner()
	if ps == nil || isNilReasoningCapability(ps.BackgroundAux) {
		return genRunner, nil, nil, nil
	}
	boundClient := ps.BackgroundAux.BindRunner(genRunner)
	poller, ok := boundClient.(auxiliary.BackgroundPoller)
	if !ok {
		return nil, nil, nil, fmt.Errorf("runtimebundle: background scheduler bound client does not implement poller")
	}
	return genRunner, boundClient, poller, nil
}
