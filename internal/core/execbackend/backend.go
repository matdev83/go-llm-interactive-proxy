// Package execbackend defines the executor-consumed outbound seam for opening
// canonical backend attempts (introduce-hexagonal-architecture). Concrete backend
// plugins and composition roots construct [Backend] values; the executor consumes
// them without importing provider or transport packages.
package execbackend

import (
	"context"
	"slices"

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
	// BackendPrefixes names this connector kind for model-inventory discovery. Prefixes may be
	// shared by instances of the same backend kind, but different backend kinds must not claim the
	// same prefix. Canonical model IDs must not use the qualifier form "<prefix>:<canonical-id>".
	BackendPrefixes []string
	// ReplaySupport is the static historical-reasoning dialect profile for this backend.
	// Prefer ResolveReplaySupport when support depends on candidate/model.
	ReplaySupport lipapi.ReasoningReplaySupport
	// ResolveReplaySupport, when set, supplies candidate/model-aware replay support; otherwise ReplaySupport is used.
	ResolveReplaySupport func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.ReasoningReplaySupport

	BillingFinalizationSupported bool
	FinalizeBilling              func(ctx context.Context, in BillingFinalizationInput) (lipapi.Event, error)

	// EnforcesMaxOutputTokens reports whether this backend serializes a
	// non-nil/positive MaxOutputTokens onto the provider wire so an authority
	// spend-cap clamp actually binds. Zero value (false) is fail-closed: the
	// executor excludes candidates that cannot represent the clamp rather than
	// opening with an unenforced limit. Backends that drop or omit the option
	// (Codex, ACP family, OpenCode, local-stub) must leave this false.
	EnforcesMaxOutputTokens bool

	ProviderCounter accountingapp.ProviderCounter

	// Close, when non-nil, releases backend-owned persistent runtime resources
	// (for example a companion process). nil means the backend owns no such
	// resources or cleans them through individual streams. Callers treat a
	// non-nil callback as idempotent; it is not a request-cancellation API.
	Close func() error
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

func EffectiveReplaySupport(
	ctx context.Context,
	be Backend,
	call lipapi.Call,
	cand routing.AttemptCandidate,
) lipapi.ReasoningReplaySupport {
	support := be.ReplaySupport
	if be.ResolveReplaySupport != nil {
		support = be.ResolveReplaySupport(ctx, call, cand)
	}
	support.Dialects = lipapi.NormalizeReasoningDialects(support.Dialects)
	return support
}

func CloneBackendPrefixes(be Backend) []string {
	return slices.Clone(be.BackendPrefixes)
}
