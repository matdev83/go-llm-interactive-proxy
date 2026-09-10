package openaicompat

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

// OpenAIResponsesProfileID is the certified profile identifier for OpenAI Responses (Requirement 4, 8).
const OpenAIResponsesProfileID = "openai_responses_v1"

func staticModelsFromInventory(inv modelinventory.Provider) (map[string]struct{}, bool) {
	if inv == nil {
		return nil, false
	}
	sp, ok := inv.(modelinventory.StaticProvider)
	if !ok || len(sp.Models) == 0 {
		return nil, false
	}
	models := make(map[string]struct{}, len(sp.Models)*2)
	for _, m := range sp.Models {
		if c := strings.TrimSpace(m.CanonicalID); c != "" {
			models[c] = struct{}{}
		}
		if n := strings.TrimSpace(m.NativeID); n != "" {
			models[n] = struct{}{}
		}
	}
	return models, true
}

func isBackendUniversal(spec BackendSpec, inv modelinventory.Provider) bool {
	if spec.WireDomainAnyAcceptedModel != nil {
		return *spec.WireDomainAnyAcceptedModel
	}
	_, hasStatic := staticModelsFromInventory(inv)
	return !hasStatic
}

func resolveResponsesWireRequest(
	ctx context.Context,
	spec BackendSpec,
	inv modelinventory.Provider,
	facts largebody.WireRequestFacts,
	cand routing.AttemptCandidate,
) largebody.WireRequestSupport {
	if ctx != nil && ctx.Err() != nil {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}
	if specFlavor(spec) != FlavorResponses {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonOperationUnsupported,
		}
	}
	if facts.Operation != lipapi.OperationOpenAIResponses {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonOperationUnsupported,
		}
	}
	if facts.ProfileID != OpenAIResponsesProfileID {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonProfileUnsupported,
		}
	}
	if facts.Delivery != lipapi.DeliveryModeStreaming {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonDeliveryUnsupported,
		}
	}
	if facts.BodyMode != largebody.BodyModeIdentityJSON {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonBodyModeUnsupported,
		}
	}

	targetModel := strings.TrimSpace(facts.CandidateModel)
	if targetModel == "" {
		targetModel = strings.TrimSpace(cand.Primary.WireModel())
	}
	if targetModel == "" {
		targetModel = strings.TrimSpace(cand.Primary.Model)
	}
	if targetModel == "" {
		targetModel = strings.TrimSpace(facts.ClientModel)
	}
	if targetModel == "" {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonModelUnsupported,
		}
	}

	staticModels, hasStatic := staticModelsFromInventory(inv)
	if hasStatic {
		if _, ok := staticModels[targetModel]; !ok {
			return largebody.WireRequestSupport{
				Compatible: false,
				Reason:     largebody.WireSupportReasonModelUnsupported,
			}
		}
	}

	needsRewrite := targetModel != strings.TrimSpace(facts.ClientModel)
	if needsRewrite && !facts.Rewrite.NeedsModelRewrite() {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonRewriteUnsupported,
		}
	}

	return largebody.WireRequestSupport{
		Compatible:        true,
		NeedsModelRewrite: needsRewrite,
		Reason:            largebody.WireSupportReasonNone,
	}
}

func resolveResponsesWireDomain(
	ctx context.Context,
	spec BackendSpec,
	inv modelinventory.Provider,
	facts largebody.WireDomainFacts,
) largebody.WireDomainSupport {
	if ctx != nil && ctx.Err() != nil {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}
	if specFlavor(spec) != FlavorResponses {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonOperationUnsupported,
		}
	}
	if facts.Operation != lipapi.OperationOpenAIResponses {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonOperationUnsupported,
		}
	}
	if facts.ProfileID != OpenAIResponsesProfileID {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonProfileUnsupported,
		}
	}
	if facts.Delivery != lipapi.DeliveryModeStreaming {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonDeliveryUnsupported,
		}
	}
	if facts.BodyMode != largebody.BodyModeIdentityJSON {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonBodyModeUnsupported,
		}
	}
	if facts.Rewrite.Kind() != largebody.RewriteKindNone && facts.Rewrite.Kind() != largebody.RewriteKindModelToken {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonRewriteUnsupported,
		}
	}

	universal := isBackendUniversal(spec, inv)
	if facts.UniversalModel {
		if !universal {
			return largebody.WireDomainSupport{
				Compatible:       false,
				AnyAcceptedModel: false,
				Reason:           largebody.WireSupportReasonModelUnsupported,
			}
		}
		return largebody.WireDomainSupport{
			Compatible:       true,
			AnyAcceptedModel: true,
			Reason:           largebody.WireSupportReasonNone,
		}
	}

	// Finite model domain: check every candidate model against declared static inventory
	if !universal {
		staticModels, _ := staticModelsFromInventory(inv)
		for _, m := range facts.CandidateModels {
			m = strings.TrimSpace(m)
			if _, ok := staticModels[m]; !ok {
				return largebody.WireDomainSupport{
					Compatible:       false,
					AnyAcceptedModel: false,
					Reason:           largebody.WireSupportReasonModelUnsupported,
				}
			}
		}
		return largebody.WireDomainSupport{
			Compatible:       true,
			AnyAcceptedModel: false,
			Reason:           largebody.WireSupportReasonNone,
		}
	}

	return largebody.WireDomainSupport{
		Compatible:       true,
		AnyAcceptedModel: true,
		Reason:           largebody.WireSupportReasonNone,
	}
}

func specFlavor(spec BackendSpec) Flavor {
	if spec.Flavor != "" {
		return spec.Flavor
	}
	if spec.ResolveFlavor != nil {
		defer func() {
			_ = recover()
		}()
		return spec.ResolveFlavor(lipapi.Call{})
	}
	return FlavorChat
}

func attachWireProof(be execbackend.Backend, spec BackendSpec, pool *credpool.Pool, inv modelinventory.Provider) execbackend.Backend {
	flavor := specFlavor(spec)
	if flavor != FlavorResponses {
		be.ResolveWireRequest = func(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
			return largebody.WireRequestSupport{
				Compatible: false,
				Reason:     largebody.WireSupportReasonOperationUnsupported,
			}
		}
		be.ResolveWireDomain = func(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
			return largebody.WireDomainSupport{
				Compatible: false,
				Reason:     largebody.WireSupportReasonOperationUnsupported,
			}
		}
		return be
	}

	be.ResolveWireRequest = func(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
		return resolveResponsesWireRequest(ctx, spec, inv, facts, cand)
	}
	be.ResolveWireDomain = func(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
		return resolveResponsesWireDomain(ctx, spec, inv, facts)
	}

	fallback := spec.RateLimitFallback
	if fallback <= 0 {
		fallback = credpool.DefaultRateLimitFallback
	}
	prims := WireOpenPrimitives{
		ProviderID:        spec.ID,
		BaseURL:           spec.BaseURL,
		Flavor:            FlavorResponses,
		Pool:              pool,
		HTTPClient:        spec.HTTPClient,
		RateLimitFallback: fallback,
		MaxPending:        50,
	}
	be.OpenWire = prims.OpenWire
	return be
}
