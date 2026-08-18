package adapter

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/processhost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

// Options configures adapter deadlines and stream bounds.
type Options struct {
	MetadataTimeout      time.Duration
	MaxPendingEvents     int
	MaxStreamFrame       int
	MaxStderrBytes       int
	CancelTimeout        time.Duration
	DisableGRPCRetry     bool
	RoutePrefixes        []string
	InstanceID           string
	EnforcesMaxOutput    bool
	Stderr               io.Reader
	InvalidateGeneration func()
	Negotiation          backendplugin.Negotiation
}

// Build constructs a composition-owned backend from a configured plugin session.
func Build(session ExecuteSession, profile backendplugin.ResolvedProfile, opt Options) *processhost.BuildResult {
	if opt.MetadataTimeout <= 0 {
		opt.MetadataTimeout = 5 * time.Second
	}
	if opt.MaxPendingEvents <= 0 {
		opt.MaxPendingEvents = 64
	}
	if opt.CancelTimeout <= 0 {
		opt.CancelTimeout = 2 * time.Second
	}
	if isZeroNegotiation(opt.Negotiation) {
		if negger, ok := session.(NegotiatedSession); ok {
			opt.Negotiation = negger.Negotiation()
		}
	}
	opt.DisableGRPCRetry = true

	prefixes := append([]string(nil), opt.RoutePrefixes...)
	if len(prefixes) == 0 {
		prefixes = append([]string(nil), profile.RoutePrefixes...)
	}

	be := execbackend.Backend{
		Caps:                         capsToLipapi(profile.Capabilities),
		DialectSupport:               backendplugin.DialectSupportToLipapi(profile.DialectSupport),
		EnforcesMaxOutputTokens:      opt.EnforcesMaxOutput || profile.EnforceMaxOutput,
		BackendPrefixes:              prefixes,
		BillingFinalizationSupported: profile.SupportsFinalizeBilling,
	}
	if be.EnforcesMaxOutputTokens {
		be.IgnoresAuthorityMaxOutputTokensClamp = execbackend.IgnoresClampViaCodexUnsupportedGenParams
	}

	be.ResolveCaps = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
		model := cand.Primary.Model
		var mid *string
		if model != "" {
			mid = &model
		}
		p, err := session.Resolve(ctx, mid)
		if err != nil {
			return be.Caps
		}
		return capsToLipapi(p.Capabilities)
	}
	be.ResolvePromptCacheProfile = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) promptcache.Profile {
		model := cand.Primary.Model
		var mid *string
		if model != "" {
			mid = &model
		}
		p, err := session.Resolve(ctx, mid)
		if err != nil {
			return promptcache.Profile{}
		}
		return p.PromptCacheProfile
	}
	be.ResolveDialectSupport = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.DialectSupport {
		model := cand.Primary.Model
		var mid *string
		if model != "" {
			mid = &model
		}
		p, err := session.Resolve(ctx, mid)
		if err != nil {
			return be.DialectSupport
		}
		return backendplugin.DialectSupportToLipapi(p.DialectSupport)
	}

	be.ResolveTransportCaps = func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) lipapi.BackendTransportCaps {
		return transportCapsFromProfile(profile, call.Invocation.Operation)
	}
	be.TransportCaps = transportCapsFromProfile(profile, "")

	if profile.ReasoningReplaySupported || profile.Capabilities.ReasoningReplay {
		be.ResolveReplaySupport = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.ReasoningReplaySupport {
			model := cand.Primary.Model
			var mid *string
			if model != "" {
				mid = &model
			}
			p, err := session.Resolve(ctx, mid)
			if err != nil {
				return lipapi.ReasoningReplaySupport{}
			}
			if !p.ReasoningReplaySupported && !p.Capabilities.ReasoningReplay {
				return lipapi.ReasoningReplaySupport{}
			}
			return lipapi.ReasoningReplaySupport{
				Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIResponsesItemV1},
			}
		}
	}

	if profile.PromptCacheProfile.RenewalSupported {
		if controller, ok := session.(OptionalPromptCacheController); ok && backendplugin.PromptCacheNegotiated(opt.Negotiation) {
			be.RenewPromptCache = controller.RenewPromptCache
			be.ReleasePromptCache = controller.ReleasePromptCache
		}
	}
	if profile.SupportsDynamicInventory {
		be.ModelInventory = &inventoryProvider{session: session, timeout: opt.MetadataTimeout, source: profile.EvidenceSource}
	}
	if profile.SupportsFinalizeBilling {
		if fin, ok := session.(OptionalBillingFinalizer); ok {
			be.FinalizeBilling = func(ctx context.Context, in execbackend.BillingFinalizationInput) (lipapi.Event, error) {
				cctx, cancel := context.WithTimeout(ctx, opt.MetadataTimeout)
				defer cancel()
				resp, err := fin.FinalizeBilling(cctx, backendplugin.FinalizeBillingRequest{
					InstanceID: opt.InstanceID, ALegID: in.ALegID, BLegID: in.BLegID,
					ModelID: in.Model, Reason: in.Reason, IdempotencyKey: finalizationIdempotencyKey(in),
				})
				if err != nil {
					return lipapi.Event{}, err
				}
				return finalizeBillingResponseToEvent(resp, finalizationIdempotencyKey(in))
			}
		}
	}
	if profile.SupportsCountTokens {
		if counter, ok := session.(OptionalTokenCounter); ok {
			be.ProviderCounter = &tokenCounterBridge{session: counter, instanceID: opt.InstanceID, timeout: opt.MetadataTimeout}
		}
	}
	be.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		return openStream(ctx, session, opt, call, cand)
	}
	return processhost.NewBuildResult(be, func() error {
		return session.Close(context.Background())
	})
}

func finalizationIdempotencyKey(in execbackend.BillingFinalizationInput) string {
	return "finalize-billing:v1:" + strings.TrimSpace(in.TraceID) + ":" + strings.TrimSpace(in.ALegID) + ":" + strings.TrimSpace(in.BLegID)
}

func transportCapsFromProfile(profile backendplugin.ResolvedProfile, op lipapi.Operation) lipapi.BackendTransportCaps {
	if op == "" {
		return nil
	}
	if !profile.Capabilities.Streaming && !profile.TransportCapabilities.BidirectionalStream {
		return nil
	}
	modes := make([]lipapi.TransportMode, 0, 2)
	if profile.Capabilities.Streaming || profile.TransportCapabilities.BidirectionalStream {
		modes = append(modes, lipapi.TransportModeStreaming)
	}
	if profile.Capabilities.Streaming {
		modes = append(modes, lipapi.TransportModeNonStreaming)
	}
	if len(modes) == 0 {
		return nil
	}
	return lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
		Operation: op,
		Modes:     modes,
	})
}

type inventoryProvider struct {
	session ExecuteSession
	timeout time.Duration
	source  string
}

func (p *inventoryProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	resp, err := p.session.ListModels(cctx, 1024)
	if err != nil {
		return modelinventory.Snapshot{}, err
	}
	out := make([]modelinventory.Model, 0, len(resp.Models))
	for _, m := range resp.Models {
		out = append(out, modelinventory.Model{
			CanonicalID: m.CanonicalModelID,
			NativeID:    m.NativeModelID,
			DisplayName: m.CanonicalModelID,
		})
	}
	src := modelinventory.Source(p.source)
	if src == "" {
		src = modelinventory.Source(resp.InventorySource)
	}
	return modelinventory.Snapshot{
		Source:   src,
		LoadedAt: time.UnixMilli(resp.FetchedUnixMS),
		Models:   out,
	}, nil
}

type tokenCounterBridge struct {
	session    OptionalTokenCounter
	instanceID string
	timeout    time.Duration
}

func (t *tokenCounterBridge) SupportsCount(_ context.Context, _ accountingapp.ProviderCountInput) accountingapp.ProviderSupport {
	return accountingapp.ProviderSupport{Status: accountingapp.SupportStatusSupported}
}

func (t *tokenCounterBridge) CountText(ctx context.Context, in accountingapp.CountTextInput) (accountingapp.CountResult, error) {
	return t.count(ctx, in.Model)
}

func (t *tokenCounterBridge) CountCall(ctx context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return t.count(ctx, in.Model)
}

func (t *tokenCounterBridge) CountOutput(ctx context.Context, in accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return t.count(ctx, in.Model)
}

func (t *tokenCounterBridge) count(ctx context.Context, model string) (accountingapp.CountResult, error) {
	cctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	resp, err := t.session.CountTokens(cctx, backendplugin.CountTokensRequest{
		InstanceID: t.instanceID,
		ModelID:    model,
		Invocation: backendplugin.Invocation{},
	})
	if err != nil {
		return accountingapp.CountResult{}, err
	}
	if !resp.Presence.InputTokens || resp.InputTokens == nil {
		return accountingapp.CountResult{}, fmt.Errorf("strict accounting: missing token evidence")
	}
	out := accountingapp.CountResult{InputTokens: int(*resp.InputTokens)}
	return out, nil
}

func isZeroNegotiation(n backendplugin.Negotiation) bool {
	return !n.Compatible && n.NegotiatedMinor == 0 && n.PluginMajor == 0 && n.PluginMinor == 0 && len(n.EnabledFeatures) == 0 && n.RejectReason == "" && n.NegotiationToken == ""
}
