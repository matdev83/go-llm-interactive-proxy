package adapter

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

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
