package reasoningcompose

import (
	"reflect"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

// Options holds trusted reasoning semantic compression composition seams.
type Options struct {
	EgressPolicies  map[string]reasoningpreservation.EgressPolicy
	MatcherResolver sdk.MatcherResolver
}

// ReasoningCompressionOptions aliases Options for source compatibility.
type ReasoningCompressionOptions = Options

// ComposeOptions combines production and testing options with production precedence.
func ComposeOptions(prod, test Options) Options {
	out := Options{}
	if !IsNilCapability(prod.MatcherResolver) {
		out.MatcherResolver = prod.MatcherResolver
	} else if !IsNilCapability(test.MatcherResolver) {
		out.MatcherResolver = test.MatcherResolver
	}
	if len(prod.EgressPolicies) == 0 && len(test.EgressPolicies) == 0 {
		return out
	}
	policies := make(map[string]reasoningpreservation.EgressPolicy, len(prod.EgressPolicies)+len(test.EgressPolicies))
	for k, v := range test.EgressPolicies {
		policies[k] = v
	}
	for k, v := range prod.EgressPolicies {
		policies[k] = v
	}
	out.EgressPolicies = policies
	return out
}

// IsNilCapability reports whether a capability value is nil or a typed nil pointer/interface.
func IsNilCapability(v any) bool {
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
