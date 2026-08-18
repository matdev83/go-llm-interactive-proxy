package backendplugin

import (
	"context"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

type PromptCacheController interface {
	RenewPromptCache(context.Context, promptcache.RenewRequest) (promptcache.RenewResponse, error)
	ReleasePromptCache(context.Context, promptcache.ReleaseRequest) error
}
type PromptCacheObservationSource interface{ promptcache.ObservationSource }

func PromptCacheNegotiated(neg Negotiation) bool {
	return neg.Compatible && neg.NegotiatedMinor >= ProtocolMinorPromptCacheResidency && slices.Contains(neg.EnabledFeatures, FeaturePromptCacheResidency)
}
