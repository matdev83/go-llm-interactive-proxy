package runtime

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	BillingRuntime
	RoutingRuntime
	SecurityRuntime
	AccountingRuntime
	ObservabilityRuntime
	ExtensionRuntime
	InterleavedRuntime
	CompactionRuntime

	LoopGuard *loopguardRuntime

	lifecycleMu     sync.Mutex
	rngOnce         sync.Once
	lockedRand      routing.Rng
	secureSessionMu sync.Mutex
	// quarantinePersistenceFault is intentional process-wide fail-closed state after a
	// secret-guard quarantine write (or SessionID invariant) failure. While latched,
	// AssertActive-before-open denies further backend dispatch on this executor until
	// process restart/reconcile. See docs/secrets-guard.md incident response.
	quarantinePersistenceFault atomic.Bool
}

func (e *Executor) capsForAttempt(
	ctx context.Context,
	be execbackend.Backend,
	attempt lipapi.Call,
	c routing.AttemptCandidate,
) lipapi.BackendCaps {
	wire := routing.BackendFacingCandidate(c)
	if e != nil && e.CapsResolver != nil {
		return e.CapsResolver.DescribeCandidate(ctx, wire, attempt)
	}
	return execbackend.EffectiveCaps(ctx, be, attempt, wire)
}

func (e *Executor) transportCapsForAttempt(
	ctx context.Context,
	be execbackend.Backend,
	attempt lipapi.Call,
	c routing.AttemptCandidate,
) lipapi.BackendTransportCaps {
	return execbackend.EffectiveTransportCaps(ctx, be, attempt, routing.BackendFacingCandidate(c))
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

// attemptOpenOwner owns pre-output candidate admission and backend stream open.
type attemptOpenOwner struct{ *Executor }

// streamAssembler owns retry-recv stream construction after a successful open.
type streamAssembler struct{ *Executor }

func (e *Executor) Execute(ctx context.Context, call *lipapi.Call) (_ lipapi.EventStream, err error) {
	prep, prepCtx, cleanup, perr := e.prepareRequest(ctx, call)
	if perr != nil {
		return nil, perr
	}
	defer func() {
		prep.finalize(err)
		cleanup()
	}()
	// Task 3.3: local-turn success has no B-leg/provider/inference billing.
	if prep.isLocal && prep.localStream != nil {
		return prep.localStream, nil
	}
	if err := e.checkCheapCredit(prepCtx, prep); err != nil {
		return nil, err
	}
	plan, err := e.buildRoutePlan(prepCtx, prep)
	if err != nil {
		return nil, err
	}
	if err := e.authorizeBillingOnce(prepCtx, prep, plan); err != nil {
		return nil, err
	}
	out, err := attemptOpenOwner{e}.openInitial(prepCtx, prep, plan)
	if err != nil {
		e.appendExposureAbortAfterAdmission(prepCtx, prep, plan)
		if out.ready == nil {
			e.notifyCompactionOpenFailed(prepCtx, prep)
		}
		return nil, err
	}
	prep.compactionOpenMeta = e.observeCompactionOpened(prepCtx, prep, out)
	stream, err := streamAssembler{e}.assemble(prepCtx, prep, plan, out)
	if err != nil {
		if out.ready != nil {
			bleg := out.ready.BLeg()
			if strings.TrimSpace(bleg.BLegID) != "" {
				e.appendPostOpenTerminalLeg(prepCtx, prep.billingCallState, prep.identity.aLeg.ALegID, bleg, out.ready.Candidate().Primary, time.Time{}, time.Time{})
			}
		}
		e.appendExposureAbortAfterAdmission(prepCtx, prep, plan)
		return nil, err
	}
	return stream, nil
}
