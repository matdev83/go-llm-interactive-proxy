package runtimebundle

import (
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	corestate "github.com/matdev83/go-llm-interactive-proxy/internal/core/state"
	coreworkspace "github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipstate "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// extensionRuntime holds the request runtime snapshot for [Built] generation.
type extensionRuntime struct {
	Snap *extensions.RequestRuntimeSnapshot
}

// buildExtensionRuntime builds the snapshot preserving exec-cell closure.
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
	var frozen lipfeature.FrozenPlaneSet
	if opts != nil {
		frozen = opts.FeaturePlanes
	}
	catalogFilters := lipfeature.Get(frozen, lipfeature.PlaneToolCatalogFilters)
	toolPolicies := lipfeature.Get(frozen, lipfeature.PlaneToolCallPolicies)
	toolFinalizers := lipfeature.Get(frozen, lipfeature.PlaneToolCallFinalizers)
	wsResolvers := lipfeature.Get(frozen, lipfeature.PlaneWorkspaceResolvers)
	var ws lipworkspace.Resolver = lipworkspace.DisabledResolver{}
	if len(wsResolvers) > 0 {
		ss := cfg.SecureSession
		secureOn := cfg.SecureSessionEffectivelyEnabled()
		resolveFailClosed := strings.ToLower(strings.TrimSpace(ss.WorkspaceResolveOnError)) == "fail_closed"
		failClosedWS := secureOn && resolveFailClosed
		if failClosedWS {
			ws = coreworkspace.NewStrictChain(wsResolvers)
		} else {
			ws = coreworkspace.NewResolverChain(wsResolvers)
		}
	}
	var openers []session.Opener
	if rawOpeners := lipfeature.Get(frozen, lipfeature.PlaneSessionOpeners); len(rawOpeners) > 0 {
		openers = rawOpeners
	}
	reqTransforms := lipfeature.Get(frozen, lipfeature.PlaneRequestTransforms)
	preReqs := lipfeature.Get(frozen, lipfeature.PlanePreRequestHandlers)
	routeHints := lipfeature.Get(frozen, lipfeature.PlaneRouteHintProviders)
	compGates := lipfeature.Get(frozen, lipfeature.PlaneCompletionGates)
	attemptXforms := lipfeature.Get(frozen, lipfeature.PlaneAttemptTransforms)
	streamObs := lipfeature.Get(frozen, lipfeature.PlaneStreamObserverFactories)
	var trafficObs traffic.Observer = traffic.NoopObserver{}
	if rawObs := lipfeature.Get(frozen, lipfeature.PlaneTrafficObservers); len(rawObs) > 0 {
		trafficObs = traffic.ChainObservers(rawObs...)
	}
	var usageObs usage.Observer = usage.NoopObserver{}
	cpUsageObs := cp.usageObserver()
	if rawUsage := lipfeature.Get(frozen, lipfeature.PlaneUsageObservers); len(rawUsage) > 0 {
		chain := make([]usage.Observer, 0, len(rawUsage)+1)
		if cpUsageObs != nil {
			chain = append(chain, cpUsageObs)
		}
		chain = append(chain, rawUsage...)
		usageObs = usage.ChainObservers(chain...)
	} else if cpUsageObs != nil {
		usageObs = cpUsageObs
	}
	var trafficRaw traffic.RawCaptureSink = traffic.DisabledRawCapture{}
	if rawSinks := lipfeature.Get(frozen, lipfeature.PlaneRawCaptureSinks); len(rawSinks) > 0 {
		trafficRaw = traffic.MultiRawCapture(rawSinks...)
	}
	trafficRedactors := lipfeature.Get(frozen, lipfeature.PlaneTrafficRedactors)
	compactionObservers := lipfeature.Get(frozen, lipfeature.PlaneCompactionObservers)
	compactionPreservers := lipfeature.Get(frozen, lipfeature.PlaneCompactionPreservers)
	localTurnHandlers := lipfeature.Get(frozen, lipfeature.PlaneLocalTurnHandlers)
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
		CompactionObservers:     compactionObservers,
		CompactionPreservers:    compactionPreservers,
		LocalTurnHandlers:       localTurnHandlers,
		FeaturePlanes:           frozen,
		SecretGuardPlane:        sgPlane,
		PolicyObserver:          policyObs,
		TimeoutBudgetSource:     budgetSrc,
	})
}
