package featurehost

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	sdkfeaturehost "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/featurehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/reasoninghost"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

// boundHostFeatures contains typed services and option models bound from
// host-provided registrations at startup (Task 8.3, Requirements 8.1, 9.3-9.6).
type boundHostFeatures struct {
	reasoning ReasoningCompressionOptions
}

// bindHostRegistrations indexes and validates startup-only host registrations.
// Concrete binding type interpretation and type switches live ONLY in this file.
// Unknown binding type/ID and duplicate semantic binding fail before serving.
func bindHostRegistrations(regs []sdkfeaturehost.Registration) (boundHostFeatures, error) {
	var out boundHostFeatures
	if len(regs) == 0 {
		return out, nil
	}
	if err := sdkfeaturehost.Validate(regs); err != nil {
		return out, err
	}

	hasReasoning := false
	for _, reg := range regs {
		switch b := reg.Binding.(type) {
		case *reasoninghost.Binding:
			if hasReasoning {
				return out, fmt.Errorf("featurehost: duplicate semantic binding for reasoning (ID %q)", b.HostBindingID())
			}
			id := strings.TrimSpace(b.HostBindingID())
			if id != reasoninghost.BindingID && id != "reasoning_compression" {
				return out, fmt.Errorf("featurehost: unknown binding ID %q for reasoning binding", id)
			}
			hasReasoning = true
			out.reasoning = adaptReasoningHostBinding(b)
		default:
			id := ""
			if reg.Binding != nil {
				id = reg.Binding.HostBindingID()
			}
			return out, fmt.Errorf("featurehost: unsupported host binding type %T (ID %q)", reg.Binding, id)
		}
	}
	return out, nil
}

// adaptReasoningHostBinding converts a public reasoninghost.Binding into the
// internal predecessor reasoningcompose.Options expected by standard generation composition.
func adaptReasoningHostBinding(b *reasoninghost.Binding) ReasoningCompressionOptions {
	if b == nil {
		return ReasoningCompressionOptions{}
	}
	out := ReasoningCompressionOptions{}
	if !isNilMatcherResolver(b.MatcherResolver) {
		out.MatcherResolver = b.MatcherResolver
	}
	if len(b.EgressPolicies) > 0 {
		m := make(map[string]reasoningpreservation.EgressPolicy, len(b.EgressPolicies))
		for k, v := range b.EgressPolicies {
			kk := strings.TrimSpace(k)
			if kk == "" {
				continue
			}
			if isNilEgressPolicy(v) {
				m[kk] = nil
				continue
			}
			m[kk] = &egressPolicyAdapter{pub: v, resolver: b.MatcherResolver}
		}
		if len(m) > 0 {
			out.EgressPolicies = m
		}
	}
	return out
}

type egressPolicyAdapter struct {
	pub      reasoninghost.EgressPolicy
	resolver sdk.MatcherResolver
}

func (a *egressPolicyAdapter) Decide(ctx context.Context, in reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	if a == nil || a.pub == nil || isNilEgressPolicy(a.pub) {
		return reasoningpreservation.CompressionEgressDecision{}, context.Canceled
	}
	sv := in.Principal.Scope()
	pubIn := reasoninghost.NewEgressInput(in.Route, in.Purpose, in.SourceClass, sv.Clone())
	dec, err := a.pub.Decide(ctx, pubIn)
	if err != nil {
		return reasoningpreservation.CompressionEgressDecision{}, err
	}
	var act reasoningpreservation.EgressAction
	switch dec.Action {
	case reasoninghost.EgressAllow:
		act = reasoningpreservation.EgressAllow
	case reasoninghost.EgressRedactThenAllow:
		act = reasoningpreservation.EgressRedactThenAllow
	default:
		act = reasoningpreservation.EgressDeny
	}
	out := reasoningpreservation.CompressionEgressDecision{
		Action:        act,
		PolicyVersion: dec.PolicyVersion,
	}
	if act == reasoningpreservation.EgressRedactThenAllow {
		if !isNilMatcherResolver(a.resolver) {
			if san := reasoningpreservation.NewResolverSanitizer(a.resolver); san != nil {
				out.Sanitizer = san
			}
		}
	}
	return out, nil
}

func isNilEgressPolicy(v reasoninghost.EgressPolicy) bool {
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

func isNilMatcherResolver(v sdk.MatcherResolver) bool {
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
