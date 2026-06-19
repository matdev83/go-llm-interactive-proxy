// Package execbackend defines the executor-consumed outbound seam for opening
// canonical backend attempts (introduce-hexagonal-architecture). Concrete backend
// plugins and composition roots construct [Backend] values; the executor consumes
// them without importing provider or transport packages.
package execbackend

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

// Backend opens a canonical event stream for one route candidate.
// Client operation and delivery metadata are carried on [lipapi.Call].Invocation.
type Backend struct {
	Caps lipapi.BackendCaps
	// ResolveCaps, when set, supplies model/candidate-aware capabilities; otherwise Caps is used.
	ResolveCaps   func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps
	TransportCaps lipapi.BackendTransportCaps
	// ResolveTransportCaps, when set, supplies model/candidate-aware transport capabilities; otherwise TransportCaps is used.
	ResolveTransportCaps func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendTransportCaps
	Open                 func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error)
	ModelInventory       modelinventory.Provider

	BillingFinalizationSupported bool
	FinalizeBilling              func(ctx context.Context, in BillingFinalizationInput) (lipapi.Event, error)

	ProviderCounter accountingapp.ProviderCounter
}

type BillingFinalizationInput struct {
	TraceID string
	ALegID  string
	BLegID  string
	Backend string
	Model   string
	Reason  string
}

// EffectiveCaps returns the caps used for negotiation for one backend and candidate.
func EffectiveCaps(
	ctx context.Context,
	be Backend,
	call lipapi.Call,
	cand routing.AttemptCandidate,
) lipapi.BackendCaps {
	if be.ResolveCaps != nil {
		return be.ResolveCaps(ctx, call, cand)
	}
	return be.Caps
}

// EffectiveTransportCaps returns the transport caps used for negotiation for one backend and candidate.
func EffectiveTransportCaps(
	ctx context.Context,
	be Backend,
	call lipapi.Call,
	cand routing.AttemptCandidate,
) lipapi.BackendTransportCaps {
	if be.ResolveTransportCaps != nil {
		return be.ResolveTransportCaps(ctx, call, cand)
	}
	return be.TransportCaps
}
