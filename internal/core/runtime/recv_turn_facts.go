package runtime

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

func (f recvTurnFacts) attemptDiagAttrs(attempt *attemptSession) diag.AttrOpts {
	attrs := diag.AttrOpts{CallID: f.traceID, BLegID: f.aLegID}
	if attempt != nil {
		attrs.BLegID = attempt.bleg.BLegID
	}
	return attrs
}

func (f recvTurnFacts) logDecision(ctx context.Context, logger *slog.Logger, key string, attrs ...slog.Attr) {
	diag.LogDecision(ctx, logger, key, diag.AttrOpts{CallID: f.traceID, BLegID: f.aLegID}, attrs...)
}

func (f recvTurnFacts) logRecoverablePreOutput(ctx context.Context, logger *slog.Logger, candidate string) {
	f.logDecision(ctx, logger, "recoverable_pre_output_swallowed", slog.String("candidate_key", candidate), slog.String("phase", "recv"))
}

func recvErrorDetail(err error) string {
	return diag.TruncErrDetail(err, attemptReasonMaxRunes)
}

// recvTurnFactsInput is the assembly-only input for the immutable request facts
// boundary. It is deliberately concrete and private; it is not a turn state bag.
type recvTurnFactsInput struct {
	baseline lipapi.Call
	traceID  string
	aLegID   string

	recvViews   execctx.Views
	recvViewsOK bool
	routePrefs  []string

	secureTurn   execctx.SecureSessionTurn
	secureTurnOK bool

	boundRegistry   modelregistry.BoundView
	boundRegistryOK bool
	boundCatalog    modelcatalog.BoundView
	boundCatalogOK  bool
	nativeResolver  routing.NativeModelResolver
	modelViewID     modelview.Identity
	modelViewIDOK   bool

	metering    *checkpoint.RequestHolder
	requestAuth *requestAuthorityState

	billingAccountID       string
	billingCustomerPricing billing.VersionRef
	billingChargePolicy    billing.VersionRef
	billingIdentityStamped bool
	billingCallID          billing.BillingCallID
	billingCallState       *billingCallState
}

// recvTurnFacts is the request-lifetime authority for facts needed after stream
// assembly. Its slices/maps are owned clones; the referenced owner pointers have
// their own lifecycle and synchronization. It intentionally contains no retry,
// event, terminal, current-attempt, or lock-bearing state.
type recvTurnFacts struct {
	baseline lipapi.Call
	traceID  string
	aLegID   string

	recvViews   execctx.Views
	recvViewsOK bool
	routePrefs  []string

	secureTurn   execctx.SecureSessionTurn
	secureTurnOK bool

	boundRegistry   modelregistry.BoundView
	boundRegistryOK bool
	boundCatalog    modelcatalog.BoundView
	boundCatalogOK  bool
	nativeResolver  routing.NativeModelResolver
	modelViewID     modelview.Identity
	modelViewIDOK   bool

	metering    *checkpoint.RequestHolder
	requestAuth *requestAuthorityState

	billingAccountID       string
	billingCustomerPricing billing.VersionRef
	billingChargePolicy    billing.VersionRef
	billingIdentityStamped bool
	billingCallID          billing.BillingCallID
	billingCallState       *billingCallState
}

func (f requestTerminalFacts) toRecvTurnFacts(ctx context.Context) recvTurnFacts {
	return newRecvTurnFacts(ctx, recvTurnFactsInput{
		baseline:               f.call,
		traceID:                f.traceID,
		aLegID:                 f.aLegID,
		secureTurn:             f.secureTurn,
		secureTurnOK:           f.secureTurnOK,
		billingCallID:          f.billingCallID,
		billingCallState:       f.billingState,
		billingAccountID:       f.accountID,
		billingCustomerPricing: f.pricing,
		billingChargePolicy:    f.chargePolicy,
		billingIdentityStamped: f.identityStamped,
		requestAuth:            f.requestAuth,
		routePrefs:             slices.Clone(f.routePrefs),
		recvViews:              f.recvViews,
		recvViewsOK:            true,
		metering:               f.metering,
	})
}

func newRecvTurnFacts(ctx context.Context, in recvTurnFactsInput) recvTurnFacts {
	f := recvTurnFacts{
		baseline:               lipapi.CloneCall(in.baseline),
		traceID:                in.traceID,
		aLegID:                 in.aLegID,
		recvViews:              cloneRecvViews(in.recvViews),
		recvViewsOK:            in.recvViewsOK,
		routePrefs:             slices.Clone(in.routePrefs),
		secureTurn:             in.secureTurn,
		secureTurnOK:           in.secureTurnOK,
		boundRegistry:          in.boundRegistry,
		boundRegistryOK:        in.boundRegistryOK,
		boundCatalog:           in.boundCatalog,
		boundCatalogOK:         in.boundCatalogOK,
		nativeResolver:         in.nativeResolver,
		modelViewID:            in.modelViewID,
		modelViewIDOK:          in.modelViewIDOK,
		metering:               in.metering,
		requestAuth:            in.requestAuth,
		billingAccountID:       in.billingAccountID,
		billingCustomerPricing: in.billingCustomerPricing,
		billingChargePolicy:    in.billingChargePolicy,
		billingIdentityStamped: in.billingIdentityStamped,
		billingCallID:          in.billingCallID,
		billingCallState:       in.billingCallState,
	}
	if f.billingCallState == nil {
		if f.billingCallID == "" {
			if callID, err := billing.NewBillingCallID(); err == nil {
				f.billingCallID = callID
			}
		}
		f.billingCallState = newBillingCallState(f.billingCallID)
	}
	f.captureBoundModelViews(ctx)
	return f
}

func (f recvTurnFacts) clone() recvTurnFacts {
	return newRecvTurnFacts(context.Background(), recvTurnFactsInput(f))
}

// captureBoundModelViews freezes the request's model publications before any
// recv-phase replacement. The source context is consulted only at assembly time.
func (f *recvTurnFacts) captureBoundModelViews(ctx context.Context) {
	if v, ok := modelregistry.BoundViewFromContext(ctx); ok {
		f.boundRegistry = v
		f.boundRegistryOK = true
	}
	if v, ok := modelcatalog.BoundViewFromContext(ctx); ok {
		f.boundCatalog = v
		f.boundCatalogOK = true
	}
	if r, ok := routing.NativeModelResolverFromContext(ctx); ok {
		f.nativeResolver = r
	}
	if id, ok := modelview.FromContext(ctx); ok {
		f.modelViewID = id
		f.modelViewIDOK = true
	}
}

// projectContext mirrors authoritative facts into the caller context for
// existing hooks, SDK seams, and diagnostics. It never reads live generation
// state and does not own cancellation or deadlines.
func (f recvTurnFacts) projectContext(parent context.Context, logger *slog.Logger) context.Context {
	ctx := diag.EnsureCallDiag(parent, f.traceID, f.aLegID)

	if f.metering != nil {
		ctx = withMeteringHolder(ctx, f.metering)
	}
	if f.requestAuth != nil {
		ctx = withRequestAuthority(ctx, f.requestAuth)
	}
	if f.recvViewsOK {
		ctx = execctx.WithViews(ctx, cloneRecvViews(f.recvViews))
	}

	if f.secureTurnOK {
		ctx = execctx.WithSecureSessionTurn(ctx, f.secureTurn)
	} else {
		// Overwrite with empty secure turn and mask any inherited policy.
		ctx = execctx.WithSecureSessionTurn(ctx, execctx.SecureSessionTurn{})
		ctx = session.WithoutSecureTurnPolicy(ctx)
	}

	if len(f.routePrefs) > 0 {
		ctx = execctx.WithRouteCandidatePreferences(ctx, slices.Clone(f.routePrefs))
	} else {
		ctx = execctx.WithoutRouteCandidatePreferences(ctx)
	}

	if f.boundRegistryOK {
		ctx = modelregistry.WithBoundView(ctx, f.boundRegistry)
	} else {
		ctx = modelregistry.WithBoundView(ctx, modelregistry.EmptyBoundView())
	}

	if f.boundCatalogOK {
		ctx = modelcatalog.WithBoundView(ctx, f.boundCatalog)
	} else {
		ctx = modelcatalog.WithBoundView(ctx, modelcatalog.EmptyBoundView())
	}

	if f.nativeResolver != nil {
		ctx = routing.WithNativeModelResolver(ctx, f.nativeResolver)
	}

	if f.modelViewIDOK {
		ctx = modelview.WithIdentity(ctx, f.modelViewID)
	} else {
		ctx = modelview.WithIdentity(ctx, modelview.Identity{})
	}

	if logger != nil {
		ctx = hooks.WithDiagnosticsLogger(ctx, logger)
	}
	return ctx
}

func (f recvTurnFacts) hookMeta(bleg b2bua.BLegRecord, cand routing.AttemptCandidate) (sdk.PartMeta, sdk.ToolMeta) {
	pm := sdk.PartMeta{
		TraceID: f.traceID, ALegID: f.aLegID, BLegID: bleg.BLegID,
		BackendID: strings.TrimSpace(cand.Primary.Backend), AttemptSeq: bleg.Seq,
	}
	tm := sdk.ToolMeta{TraceID: f.traceID, ALegID: f.aLegID, BLegID: bleg.BLegID, AttemptSeq: bleg.Seq}
	if v, ok := f.viewsFor(nil); ok { //nolint:staticcheck // intentional nil context forces stream snapshot fallback
		tm.Principal, tm.Scope, tm.Session, tm.Workspace = v.Principal, v.Scope, v.Session, v.Workspace
	}
	return pm, tm
}

func (f recvTurnFacts) viewsFor(ctx context.Context) (execctx.Views, bool) {
	// Frozen facts are authoritative; context contributes only cancellation, deadline,
	// tracing, diagnostics. Ignore any execctx.Views in the caller context for authoritative keys
	// and return the frozen recvViews directly.
	if f.recvViewsOK {
		return cloneRecvViews(f.recvViews), true
	}
	if v, ok := execctx.FromContext(ctx); ok {
		return v, true
	}
	return execctx.Views{}, false
}

func cloneRecvViews(v execctx.Views) execctx.Views {
	v.Principal.Claims = maps.Clone(v.Principal.Claims)
	v.Principal.Roles = slices.Clone(v.Principal.Roles)
	v.Scope = v.Scope.Clone()
	v.Session.Labels = maps.Clone(v.Session.Labels)
	v.Workspace.Labels = maps.Clone(v.Workspace.Labels)
	v.Workspace.Markers = slices.Clone(v.Workspace.Markers)
	v.Annotations = maps.Clone(v.Annotations)
	return v
}
