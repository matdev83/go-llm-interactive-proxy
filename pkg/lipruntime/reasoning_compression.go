package lipruntime

import (
	"context"
	"reflect"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

// EgressAction is the bounded public egress decision.
// Mirrors internal reasoningpreservation.EgressAction but is host-owned.
type EgressAction uint8

const (
	EgressDeny EgressAction = iota
	EgressAllow
	EgressRedactThenAllow
)

func (a EgressAction) String() string {
	switch a {
	case EgressAllow:
		return "allow"
	case EgressRedactThenAllow:
		return "redact_then_allow"
	default:
		return "deny"
	}
}

// EgressInput is the narrow public egress policy input.
// Scope is held privately and returned as defensive clone via Scope().
type EgressInput struct {
	Route       string
	Purpose     string
	SourceClass string
	scope       scope.PrincipalScopeView
}

// Scope returns a defensive clone of the principal scope.
func (i EgressInput) Scope() scope.PrincipalScopeView { return i.scope.Clone() }

// NewEgressInput constructs a public input with defensive clone of scope.
// Used by adapters and tests.
func NewEgressInput(route, purpose, sourceClass string, s scope.PrincipalScopeView) EgressInput {
	return EgressInput{Route: route, Purpose: purpose, SourceClass: sourceClass, scope: s.Clone()}
}

// EgressDecision is the trusted public policy decision.
type EgressDecision struct {
	Action        EgressAction
	PolicyVersion string
}

// EgressPolicy is the trusted public data-egress decision seam for host composition.
// It is not money and is injected via Options.ReasoningCompression.EgressPolicies.
// Stock lipstd does not invent a policy; enabled compression without host policy fails closed.
type EgressPolicy interface {
	Decide(ctx context.Context, in EgressInput) (EgressDecision, error)
}

// ReasoningCompressionOptions holds trusted reasoning semantic compression composition seams.
// This is the public host seam for external binaries (lipruntime). Stock lipstd leaves it zero
// and fails closed if compression is enabled without host-provided policy (supported host path is via lipruntime).
type ReasoningCompressionOptions struct {
	EgressPolicies  map[string]EgressPolicy
	MatcherResolver sdk.MatcherResolver
}

func adaptReasoningCompressionOptions(pub ReasoningCompressionOptions) runtimebundle.ReasoningCompressionOptions {
	out := runtimebundle.ReasoningCompressionOptions{}
	if isNilMatcherResolver(pub.MatcherResolver) {
		out.MatcherResolver = nil
	} else {
		out.MatcherResolver = pub.MatcherResolver
	}
	if len(pub.EgressPolicies) > 0 {
		m := make(map[string]reasoningpreservation.EgressPolicy, len(pub.EgressPolicies))
		for k, v := range pub.EgressPolicies {
			kk := strings.TrimSpace(k)
			if kk == "" {
				continue
			}
			if isNilEgressPolicy(v) {
				m[kk] = nil
				continue
			}
			m[kk] = &egressPolicyAdapter{pub: v, resolver: pub.MatcherResolver}
		}
		if len(m) > 0 {
			out.EgressPolicies = m
		}
	}
	return out
}

type egressPolicyAdapter struct {
	pub      EgressPolicy
	resolver sdk.MatcherResolver
}

func (a *egressPolicyAdapter) Decide(ctx context.Context, in reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	if isNilEgressPolicy(a.pub) {
		return reasoningpreservation.CompressionEgressDecision{}, context.Canceled // triggers missing-policy in EvaluateEgress via err
	}
	// Defensive clone of scope from internal view.
	sv := in.Principal.Scope()
	pubIn := EgressInput{Route: in.Route, Purpose: in.Purpose, SourceClass: in.SourceClass, scope: sv.Clone()}
	dec, err := a.pub.Decide(ctx, pubIn)
	if err != nil {
		return reasoningpreservation.CompressionEgressDecision{}, err
	}
	var act reasoningpreservation.EgressAction
	switch dec.Action {
	case EgressDeny:
		act = reasoningpreservation.EgressDeny
	case EgressAllow:
		act = reasoningpreservation.EgressAllow
	case EgressRedactThenAllow:
		act = reasoningpreservation.EgressRedactThenAllow
	default:
		act = reasoningpreservation.EgressDeny
	}
	out := reasoningpreservation.CompressionEgressDecision{Action: act, PolicyVersion: dec.PolicyVersion}
	if act == reasoningpreservation.EgressRedactThenAllow {
		// Supply trusted resolver sanitizer so existing EvaluateEgress does not prematurely deny.
		// Runtime stage still overrides with trusted service sanitizer.
		if !isNilMatcherResolver(a.resolver) {
			if san := reasoningpreservation.NewResolverSanitizer(a.resolver); san != nil {
				out.Sanitizer = san
			}
		}
	}
	return out, nil
}

func isNilEgressPolicy(v EgressPolicy) bool {
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
