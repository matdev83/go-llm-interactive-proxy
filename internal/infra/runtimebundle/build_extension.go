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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
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
func buildExtensionRuntime(bctx buildContext, nowFn func() time.Time, execRunnerProvider func() auxreq.ExecutorRunner, cp *controlPlaneRuntime) *extensionRuntime {
	snap := buildRuntimeSnapshot(bctx.Bus, bctx.Cfg, bctx.Opts, nowFn, execRunnerProvider, cp)
	return &extensionRuntime{Snap: snap}
}

func buildRuntimeSnapshot(
	bus *hooks.Bus,
	cfg *config.Config,
	opts *BuildOptions,
	nowFn func() time.Time,
	execRunnerProvider func() auxreq.ExecutorRunner,
	cp *controlPlaneRuntime,
) *extensions.RequestRuntimeSnapshot {
	var ws lipworkspace.Resolver = lipworkspace.DisabledResolver{}
	if len(opts.WorkspaceResolvers) > 0 {
		ss := cfg.SecureSession
		secureOn := cfg.SecureSessionEffectivelyEnabled()
		resolveFailClosed := strings.ToLower(strings.TrimSpace(ss.WorkspaceResolveOnError)) == "fail_closed"
		failClosedWS := secureOn && resolveFailClosed
		if failClosedWS {
			ws = coreworkspace.NewStrictChain(opts.WorkspaceResolvers)
		} else {
			ws = coreworkspace.NewResolverChain(opts.WorkspaceResolvers)
		}
	}
	var openers []session.Opener
	if len(opts.SessionOpeners) > 0 {
		openers = slices.Clone(opts.SessionOpeners)
	}
	var catalogFilters []toolcatalog.Filter
	if len(opts.ToolCatalogFilters) > 0 {
		catalogFilters = slices.Clone(opts.ToolCatalogFilters)
	}
	var toolPolicies []toolpolicy.Policy
	if len(opts.ToolCallPolicies) > 0 {
		toolPolicies = slices.Clone(opts.ToolCallPolicies)
	}
	var reqTransforms []request.Transform
	if len(opts.RequestTransforms) > 0 {
		reqTransforms = slices.Clone(opts.RequestTransforms)
	}
	var preReqs []prerequest.Handler
	if len(opts.PreRequestHandlers) > 0 {
		preReqs = slices.Clone(opts.PreRequestHandlers)
	}
	var routeHints []routehint.Provider
	if len(opts.RouteHintProviders) > 0 {
		routeHints = slices.Clone(opts.RouteHintProviders)
	}
	var compGates []completion.Gate
	if len(opts.CompletionGates) > 0 {
		compGates = slices.Clone(opts.CompletionGates)
	}
	var trafficObs traffic.Observer = traffic.NoopObserver{}
	if len(opts.TrafficObservers) > 0 {
		trafficObs = traffic.ChainObservers(opts.TrafficObservers...)
	}
	var usageObs usage.Observer = usage.NoopObserver{}
	cpUsageObs := cp.usageObserver()
	if len(opts.UsageObservers) > 0 {
		chain := make([]usage.Observer, 0, len(opts.UsageObservers)+1)
		if cpUsageObs != nil {
			chain = append(chain, cpUsageObs)
		}
		chain = append(chain, opts.UsageObservers...)
		usageObs = usage.ChainObservers(chain...)
	} else if cpUsageObs != nil {
		usageObs = cpUsageObs
	}
	var trafficRaw traffic.RawCaptureSink = traffic.DisabledRawCapture{}
	if len(opts.RawCaptureSinks) > 0 {
		trafficRaw = traffic.MultiRawCapture(opts.RawCaptureSinks...)
	}
	var trafficRedactors []traffic.Redactor
	if len(opts.TrafficRedactors) > 0 {
		trafficRedactors = slices.Clone(opts.TrafficRedactors)
	}
	var policyObs policydecision.Observer = policydecision.NoopObserver{}
	cpPolicyObs := cp.policyObserver()
	if len(opts.PolicyObservers) > 0 {
		chain := make([]policydecision.Observer, 0, len(opts.PolicyObservers)+1)
		if cpPolicyObs != nil {
			chain = append(chain, cpPolicyObs)
		}
		chain = append(chain, opts.PolicyObservers...)
		policyObs = policydecision.NewChainObserver(chain...)
	} else if cpPolicyObs != nil {
		policyObs = cpPolicyObs
	}
	var budgetSrc extensions.TimeoutBudgetSource = extensions.DefaultTimeoutBudgetSource{}
	if opts.PolicyTimeoutBudgetSource != nil {
		budgetSrc = opts.PolicyTimeoutBudgetSource
	}
	return extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		State:               corestate.NewMem(nowFn),
		Aux:                 auxreq.NewClient(execRunnerProvider),
		Workspace:           ws,
		SessionOpeners:      openers,
		ToolCatalogFilters:  catalogFilters,
		ToolCallPolicies:    toolPolicies,
		RequestTransforms:   reqTransforms,
		PreRequestHandlers:  preReqs,
		RouteHintProviders:  routeHints,
		CompletionGates:     compGates,
		TrafficObserver:     trafficObs,
		UsageObserver:       usageObs,
		RawCapture:          trafficRaw,
		TrafficRedactors:    trafficRedactors,
		PolicyObserver:      policyObs,
		TimeoutBudgetSource: budgetSrc,
	})
}
