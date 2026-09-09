package largebody

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// LargeBodyExecutor is the internal optional large-body capability for the
// large-payload streaming fast path (design section 8, assessor/executor
// interface).
//
// It is deliberately NOT part of the public lipsdk.ExecutorView contract
// (Requirements 1, 22): existing public ExecutorView remains supported and
// unchanged, and external/manual frontends/executors stay source-compatible
// and canonical-only. The standard bundled frontend path type-asserts this
// capability from its lipsdk.ExecutorView; absence selects the canonical path
// with no behavior change.
//
// Ownership (steering structure SDK zones, product core/plugin ownership):
// provider-neutral DTOs and this seam live in internal core; provider/frontend
// semantics stay in adapters/plugins. Core never imports concrete plugins or
// provider SDKs here (stdlib plus pkg/lipsdk only).
type LargeBodyExecutor interface {
	// AssessLargeBody is side-effect-free proof assessment over bounded facts.
	AssessLargeBody(ctx context.Context, req AssessmentRequest) (AssessmentResult, error)
	// ExecuteLargeBody runs the accepted wire turn from the bound stamp and
	// immutable replay source. It is reachable only after assessment accepts.
	ExecuteLargeBody(ctx context.Context, stamp AssessmentStamp, src Source) (ExecutionResult, error)
}

// AsLargeBodyExecutor safely probes the standard frontend executor for the
// internal large-body capability. A nil executor or an executor without the
// capability reports ok=false so the caller continues on the canonical path.
func AsLargeBodyExecutor(exec lipsdk.ExecutorView) (LargeBodyExecutor, bool) {
	if exec == nil {
		return nil, false
	}
	capability, ok := exec.(LargeBodyExecutor)
	if !ok || capability == nil {
		return nil, false
	}
	return capability, true
}
