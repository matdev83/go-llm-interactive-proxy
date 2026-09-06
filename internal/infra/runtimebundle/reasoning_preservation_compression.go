package runtimebundle

import (
	"fmt"
	"reflect"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactioncompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

// reasoningCompressionProductionOptions returns the raw production reasoning
// options source. The facade merges production/testing internally; generic
// runtimebundle never merges or interprets reasoning policy (Task 2.4).
func reasoningCompressionProductionOptions(ps *ProcessServices) featurehost.ReasoningCompressionOptions {
	if ps == nil || ps.opts == nil {
		return featurehost.ReasoningCompressionOptions{}
	}
	return ps.opts.Production.ReasoningCompression
}

// reasoningCompressionTestingOptions returns the raw testing reasoning
// options source. See reasoningCompressionProductionOptions.
func reasoningCompressionTestingOptions(ps *ProcessServices) featurehost.ReasoningCompressionOptions {
	if ps == nil || ps.opts == nil {
		return featurehost.ReasoningCompressionOptions{}
	}
	return ps.opts.Testing.ReasoningCompression
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
