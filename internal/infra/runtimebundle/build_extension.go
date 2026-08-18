package runtimebundle

import (
	"slices"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	corestate "github.com/matdev83/go-llm-interactive-proxy/internal/core/state"
	coreworkspace "github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/prerequest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipstate "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// extensionRuntime holds the request runtime snapshot produced by
// [buildExtensionRuntime]. The snapshot is immutable for the lifetime of the
// [Built] generation.
type extensionRuntime struct {
	Snap *extensions.RequestRuntimeSnapshot
}

// buildExtensionRuntime builds the request runtime snapshot, preserving the
// exec-cell closure: the caller declares the executor cell and passes a provider
// so the snapshot's auxiliary client can resolve the executor after it is
// constructed (snapshot is built before the executor; see [Build]).
//
// policyObs is the pre-assembled policy observer chain (operator observers plus
// the control-plane policy observer adapter, when enabled). It is assembled once
// in [Build] via [assemblePolicyObserverChain] so the usage-authority evidence
// sink can fan to the same chain instance the runtime snapshot uses, avoiding
// duplicate control-plane events for authority decisions.
func buildExtensionRuntime(bctx buildContext, nowFn func() time.Time, execRunnerProvider func() auxreq.ExecutorRunner, cp *controlPlaneRuntime, policyObs policydecision.Observer, sg *secretGuardRuntime, extensionState lipstate.Store) *extensionRuntime {
	var plane extensions.SecretGuardPlane
	if sg != nil {
		plane = sg.Plane
	}
	snap := buildRuntimeSnapshot(bctx.Bus, bctx.Cfg, bctx.Opts, nowFn, execRunnerProvider, cp, policyObs, plane, extensionState)
	return &extensionRuntime{Snap: snap}
}

// assemblePolicyObserverChain builds the policydecision observer chain shared by
// the runtime snapshot and the usage-authority evidence sink. The control-plane
// policy observer adapter is prepended (when enabled) so authority decisions
// project into the control-plane ledger as CategoryPolicy events alongside
// operator-supplied observers. Returns NoopObserver when neither is configured.
func assemblePolicyObserverChain(opts *BuildOptions, cp *controlPlaneRuntime) policydecision.Observer {
	var policyObs policydecision.Observer = policydecision.NoopObserver{}
	cpPolicyObs := cp.policyObserver()
	var operators []policydecision.Observer
	if opts != nil {
		operators = append(operators, opts.Policy.PolicyObservers...)
		operators = append(operators, opts.Production.PolicyObservers...)
	}
	if len(operators) > 0 {
		chain := make([]policydecision.Observer, 0, len(operators)+1)
		if cpPolicyObs != nil {
			chain = append(chain, cpPolicyObs)
		}
		chain = append(chain, operators...)
		policyObs = policydecision.NewChainObserver(chain...)
	} else if cpPolicyObs != nil {
		policyObs = cpPolicyObs
	}
	return policyObs
}

func buildRuntimeSnapshot(
	bus *hooks.Bus,
	cfg *config.Config,
	opts *BuildOptions,
	nowFn func() time.Time,
	execRunnerProvider func() auxreq.ExecutorRunner,
	cp *controlPlaneRuntime,
	policyObs policydecision.Observer,
	sgPlane extensions.SecretGuardPlane,
	extensionState lipstate.Store,
) *extensions.RequestRuntimeSnapshot {
	var ws lipworkspace.Resolver = lipworkspace.DisabledResolver{}
	if len(opts.Extensions.WorkspaceResolvers) > 0 {
		ss := cfg.SecureSession
		secureOn := cfg.SecureSessionEffectivelyEnabled()
		resolveFailClosed := strings.ToLower(strings.TrimSpace(ss.WorkspaceResolveOnError)) == "fail_closed"
		failClosedWS := secureOn && resolveFailClosed
		if failClosedWS {
			ws = coreworkspace.NewStrictChain(opts.Extensions.WorkspaceResolvers)
		} else {
			ws = coreworkspace.NewResolverChain(opts.Extensions.WorkspaceResolvers)
		}
	}
	var openers []session.Opener
	if len(opts.Extensions.SessionOpeners) > 0 {
		openers = slices.Clone(opts.Extensions.SessionOpeners)
	}
	var catalogFilters []toolcatalog.Filter
	if len(opts.Extensions.ToolCatalogFilters) > 0 {
		catalogFilters = slices.Clone(opts.Extensions.ToolCatalogFilters)
	}
	var toolPolicies []toolpolicy.Policy
	if len(opts.Extensions.ToolCallPolicies) > 0 {
		toolPolicies = slices.Clone(opts.Extensions.ToolCallPolicies)
	}
	var toolFinalizers []toolcall.Finalizer
	if len(opts.Extensions.ToolCallFinalizers) > 0 {
		toolFinalizers = slices.Clone(opts.Extensions.ToolCallFinalizers)
	}
	var reqTransforms []request.Transform
	if len(opts.Extensions.RequestTransforms) > 0 {
		reqTransforms = slices.Clone(opts.Extensions.RequestTransforms)
	}
	var preReqs []prerequest.Handler
	if len(opts.Extensions.PreRequestHandlers) > 0 {
		preReqs = slices.Clone(opts.Extensions.PreRequestHandlers)
	}
	var routeHints []routehint.Provider
	if len(opts.Extensions.RouteHintProviders) > 0 {
		routeHints = slices.Clone(opts.Extensions.RouteHintProviders)
	}
	var compGates []completion.Gate
	if len(opts.Extensions.CompletionGates) > 0 {
		compGates = slices.Clone(opts.Extensions.CompletionGates)
	}
	var attemptXforms []request.AttemptTransform
	if len(opts.Extensions.AttemptTransforms) > 0 {
		attemptXforms = slices.Clone(opts.Extensions.AttemptTransforms)
	}
	var streamObs []response.StreamObserverFactory
	if len(opts.Extensions.StreamObserverFactories) > 0 {
		streamObs = slices.Clone(opts.Extensions.StreamObserverFactories)
	}
	var trafficObs traffic.Observer = traffic.NoopObserver{}
	if len(opts.Extensions.TrafficObservers) > 0 {
		trafficObs = traffic.ChainObservers(opts.Extensions.TrafficObservers...)
	}
	var usageObs usage.Observer = usage.NoopObserver{}
	cpUsageObs := cp.usageObserver()
	if len(opts.Extensions.UsageObservers) > 0 {
		chain := make([]usage.Observer, 0, len(opts.Extensions.UsageObservers)+1)
		if cpUsageObs != nil {
			chain = append(chain, cpUsageObs)
		}
		chain = append(chain, opts.Extensions.UsageObservers...)
		usageObs = usage.ChainObservers(chain...)
	} else if cpUsageObs != nil {
		usageObs = cpUsageObs
	}
	var trafficRaw traffic.RawCaptureSink = traffic.DisabledRawCapture{}
	if len(opts.Extensions.RawCaptureSinks) > 0 {
		trafficRaw = traffic.MultiRawCapture(opts.Extensions.RawCaptureSinks...)
	}
	var trafficRedactors []traffic.Redactor
	if len(opts.Extensions.TrafficRedactors) > 0 {
		trafficRedactors = slices.Clone(opts.Extensions.TrafficRedactors)
	}
	var budgetSrc extensions.TimeoutBudgetSource = extensions.DefaultTimeoutBudgetSource{}
	if opts.Policy.PolicyTimeoutBudgetSource != nil {
		budgetSrc = opts.Policy.PolicyTimeoutBudgetSource
	}
	stateStore := extensionState
	if stateStore == nil {
		stateStore = corestate.NewMem(nowFn)
	}
	return extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		State:                   stateStore,
		Aux:                     auxreq.NewClient(execRunnerProvider),
		Workspace:               ws,
		SessionOpeners:          openers,
		ToolCatalogFilters:      catalogFilters,
		ToolCallPolicies:        toolPolicies,
		ToolCallFinalizers:      toolFinalizers,
		RequestTransforms:       reqTransforms,
		PreRequestHandlers:      preReqs,
		RouteHintProviders:      routeHints,
		CompletionGates:         compGates,
		AttemptTransforms:       attemptXforms,
		StreamObserverFactories: streamObs,
		TrafficObserver:         trafficObs,
		UsageObserver:           usageObs,
		RawCapture:              trafficRaw,
		TrafficRedactors:        trafficRedactors,
		CompactionObservers:     opts.Extensions.CompactionObservers,
		SecretGuardPlane:        sgPlane,
		PolicyObserver:          policyObs,
		TimeoutBudgetSource:     budgetSrc,
	})
}
