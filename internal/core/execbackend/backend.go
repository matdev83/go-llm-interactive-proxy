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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
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

	// DialectSupport declares exact item/reasoning/compaction/extension dialects this backend satisfies.
	DialectSupport lipapi.DialectSupport
	// ResolveDialectSupport, when set, supplies candidate/model-aware dialect support; otherwise DialectSupport is used.
	ResolveDialectSupport func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.DialectSupport

	// ResolvePromptCacheProfile supplies model/candidate-aware provider-neutral
	// residency capability. nil means observation/control unsupported.
	ResolvePromptCacheProfile func(context.Context, lipapi.Call, routing.AttemptCandidate) promptcache.Profile
	// RenewPromptCache and ReleasePromptCache are direct operations on an
	// already-issued backend-owned handle. They never receive a selector and
	// must not invoke ordinary route selection or inference.
	RenewPromptCache   func(context.Context, promptcache.RenewRequest) (promptcache.RenewResponse, error)
	ReleasePromptCache func(context.Context, promptcache.ReleaseRequest) error

	BillingFinalizationSupported bool
	FinalizeBilling              func(ctx context.Context, in BillingFinalizationInput) (lipapi.Event, error)

	// EnforcesMaxOutputTokens reports whether this backend serializes a
	// non-nil/positive MaxOutputTokens onto the provider wire so an authority
	// spend-cap clamp actually binds. Zero value (false) is fail-closed: the
	// executor excludes candidates that cannot represent the clamp rather than
	// opening with an unenforced limit. Backends that drop or omit the option
	// (Codex, ACP family, OpenCode, local-stub) must leave this false.
	EnforcesMaxOutputTokens bool
	// IgnoresAuthorityMaxOutputTokensClamp, when set, reports call-specific reasons
	// the backend drops MaxOutputTokens despite EnforcesMaxOutputTokens.
	IgnoresAuthorityMaxOutputTokensClamp func(call lipapi.Call) bool

	ProviderCounter accountingapp.ProviderCounter
	// LocalCounter, when set, supplies instance-local tokenizer counting for
	// compatible modes configured with an explicit tokenizer override.
	LocalCounter accountingapp.LocalCounter
	// TokenizerID is the bounded configured local tokenizer identifier exposed
	// for diagnostics and accounting when LocalCounter is attached.
	TokenizerID string

	// Close, when non-nil, releases backend-owned persistent runtime resources
	// (for example a companion process). nil means the backend owns no such
	// resources or cleans them through individual streams. Callers treat a
	// non-nil callback as idempotent; it is not a request-cancellation API.
	Close func() error

	// Optional generation-local lifecycle / transport seams. nil means unsupported.
	// Composition roots adapt these into candidate prepare/rollback ownership;
	// legacy backends that only set Close remain fully compatible.
	Start                 func(context.Context) error
	Stop                  func(context.Context) error
	CleanupIdleTransports func(context.Context) error
	// PreflightCapability, when set, is an explicit non-billable readiness probe.
	// It is never invoked automatically as a publication gate.
	PreflightCapability func(context.Context) (CapabilityPreflight, error)
}

// CapabilityPreflight is an optional non-billable backend readiness probe result.
// Billable must be false when consumed by candidate composition (req 8.11).
type CapabilityPreflight struct {
	Ready       bool
	Billable    bool
	Description string
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

func EffectiveDialectSupport(
	ctx context.Context,
	be Backend,
	call lipapi.Call,
	cand routing.AttemptCandidate,
) lipapi.DialectSupport {
	support := be.DialectSupport
	if be.ResolveDialectSupport != nil {
		support = be.ResolveDialectSupport(ctx, call, cand)
	}
	replay := EffectiveReplaySupport(ctx, be, call, cand)
	for _, d := range replay.Dialects {
		support.ReasoningDialects = append(support.ReasoningDialects, lipapi.DialectRequirement{
			Kind:    "reasoning",
			Dialect: string(lipapi.NormalizeReasoningDialect(d)),
		})
	}
	return lipapi.NormalizeDialectSupport(support)
}

func CloneBackendPrefixes(be Backend) []string {
	return slices.Clone(be.BackendPrefixes)
}

// EffectivePromptCacheProfile resolves capability for the selected effective
// model/candidate. A nil resolver is intentionally observation/control unknown.
func EffectivePromptCacheProfile(ctx context.Context, be Backend, call lipapi.Call, cand routing.AttemptCandidate) promptcache.Profile {
	if be.ResolvePromptCacheProfile == nil {
		return promptcache.Profile{}
	}
	profile := be.ResolvePromptCacheProfile(ctx, call, cand)
	if normalized, err := profile.Normalize(); err == nil {
		return normalized
	}
	return promptcache.Profile{}
}

// DrainPromptCacheObservations drains only the optional host-only sideband.
// Implementations decide whether a successful terminal committed the buffer.
func DrainPromptCacheObservations(stream lipapi.ManagedEventStream) []promptcache.Observation {
	if stream == nil {
		return nil
	}
	source, ok := stream.(promptcache.ObservationSource)
	if !ok {
		return nil
	}
	return source.DrainPromptCacheObservations()
}
