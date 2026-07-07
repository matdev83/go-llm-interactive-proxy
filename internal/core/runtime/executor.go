package runtime

import (
	"context"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

var _ lipsdk.ExecutorView = (*Executor)(nil)

// noCopy signals go vet's copylocks analyzer to reject accidental copies.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// secureSessionTestPrepare is a no-op in production and in packages that import runtime without compiling
// runtime's *_test.go sources (for example tests under internal/core/runtime/failclosed). Only the
// internal/core/runtime test binary links export_test.go, which assigns this hook in init.
var secureSessionTestPrepare = func(*Executor) {}

// Executor orchestrates hooks, capability negotiation, routing, B2BUA, and backend attempts.
// Fields are grouped by concern; promoted fields preserve the historical flat access API.
type Executor struct {
	_ noCopy //nolint:unused
	CoreRuntime
	RoutingRuntime
	SecurityRuntime
	AccountingRuntime
	ObservabilityRuntime
	ExtensionRuntime
	InterleavedRuntime

	lifecycleMu     sync.Mutex
	rngOnce         sync.Once
	lockedRand      routing.Rng
	secureSessionMu sync.Mutex
}

func (e *Executor) capsForAttempt(
	ctx context.Context,
	be execbackend.Backend,
	attempt lipapi.Call,
	c routing.AttemptCandidate,
) lipapi.BackendCaps {
	if e != nil && e.CapsResolver != nil {
		return e.CapsResolver.DescribeCandidate(ctx, c, attempt)
	}
	return execbackend.EffectiveCaps(ctx, be, attempt, c)
}

func (e *Executor) transportCapsForAttempt(
	ctx context.Context,
	be execbackend.Backend,
	attempt lipapi.Call,
	c routing.AttemptCandidate,
) lipapi.BackendTransportCaps {
	return execbackend.EffectiveTransportCaps(ctx, be, attempt, c)
}

func (e *Executor) effectiveTransportFallbackPolicy() lipapi.TransportFallbackPolicy {
	if e == nil || e.TransportFallbackPolicy == "" {
		return lipapi.TransportFallbackCompatibility
	}
	return e.TransportFallbackPolicy
}

// policyEvidenceEmitter builds an EvidenceEmitter from the snapshot's policy observer
// and the executor logger. Returns nil when snap is nil, or when the snapshot's policy
// observer is the no-op default and privileged diagnostics are not enabled, so the
// no-observer deployment does not attach an active emitter, build decision contexts,
// or emit policy logs (requirements 7.6, 10.5). Emit on a nil emitter is a no-op, so
// callers can always invoke it without nil checks. The emitter is built per-call to
// reflect the frozen snapshot without caching across generations.
func (e *Executor) policyEvidenceEmitter(snap *extensions.RequestRuntimeSnapshot) *extensions.EvidenceEmitter {
	if e == nil || snap == nil {
		return nil
	}
	obs := snap.PolicyObserver()
	if policydecision.IsNoopObserver(obs) && !e.PolicyDiagnosticsEnabled {
		return nil
	}
	return extensions.NewEvidenceEmitter(obs, e.Log, e.PolicyDiagnosticsEnabled)
}

// Execute runs submit hooks, resolves the A-leg, plans routes, negotiates per attempt,
// and returns a stream. Recoverable pre-output failures may consume additional B-legs
// before the returned stream yields events.
//
// ctx must be non-nil (same contract as [lipapi.EventStream.Recv]); nil returns [lipapi.ErrNilContext].
const otelScopeExecutor = "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"

func (e *Executor) Execute(ctx context.Context, call *lipapi.Call) (_ lipapi.EventStream, err error) {
	prep, cleanup, perr := e.prepareRequest(ctx, call)
	if perr != nil {
		return nil, perr
	}
	defer func() {
		prep.finalize(err)
		cleanup()
	}()

	plan, err := e.buildRoutePlan(prep)
	if err != nil {
		return nil, err
	}
	out, err := e.openInitialAttempt(prep, plan)
	if err != nil {
		return nil, err
	}
	return e.assembleExecutorStream(prep, plan, out)
}
