package adapter

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	publichost "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/host"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

// GRPCSession is retained as an internal compatibility alias. The supported
// implementation and lifecycle authority live in the public host package.
type GRPCSession = publichost.Session

// ExecuteSession is the host-facing configured plugin instance seam.
type ExecuteSession interface {
	Resolve(ctx context.Context, modelID *string) (backendplugin.ResolvedProfile, error)
	ListModels(ctx context.Context, maxModels uint32) (backendplugin.ListModelsResponse, error)
	Execute(stream backendplugin.ExecuteStream) error
	Close(ctx context.Context) error
}

// NegotiatedSession exposes the protocol negotiation outcome bound at dial time.
type NegotiatedSession interface {
	ExecuteSession
	Negotiation() backendplugin.Negotiation
}

// OptionalTokenCounter is satisfied when counting is advertised.
type OptionalTokenCounter interface {
	CountTokens(ctx context.Context, req backendplugin.CountTokensRequest) (backendplugin.CountTokensResponse, error)
}

// OptionalBillingFinalizer is satisfied when billing finalization is advertised.
type OptionalBillingFinalizer interface {
	FinalizeBilling(ctx context.Context, req backendplugin.FinalizeBillingRequest) (backendplugin.FinalizeBillingResponse, error)
}

// OptionalPromptCacheController is satisfied only when the connector negotiated
// and implemented the dedicated maintenance plane.
type OptionalPromptCacheController interface {
	RenewPromptCache(context.Context, promptcache.RenewRequest) (promptcache.RenewResponse, error)
	ReleasePromptCache(context.Context, promptcache.ReleaseRequest) error
}

var (
	_ OptionalTokenCounter          = (*GRPCSession)(nil)
	_ OptionalBillingFinalizer      = (*GRPCSession)(nil)
	_ OptionalPromptCacheController = (*GRPCSession)(nil)
)
