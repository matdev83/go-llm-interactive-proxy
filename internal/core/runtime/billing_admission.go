package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

var ErrBillingAdmissionDenied = errors.New("executor: billing admission denied")

// BillingAdmissionInput is the side-effect-free route-plan view passed to the
// billing adapter. It contains canonical request values and no provider SDK or
// database objects. The adapter may estimate and reserve, but must not perform
// upstream provider/process work.
type BillingAdmissionInput struct {
	Call        lipapi.Call
	TraceID     string
	ALegID      string
	Route       *routing.Selector
	RequestSize routing.RequestSizeEstimate
}

// BillingAdmission is the only runtime financial admission capability. Runtime
// invokes it once after route planning and before the first provider/connector
// operation. Post-turn settlement is deliberately not part of this port.
type BillingAdmission interface {
	Authorize(context.Context, BillingAdmissionInput) (billing.Authorization, error)
}

// BillingAdmissionCleanup releases an unused authorization hold when Execute
// fails before any request-terminal owner can seal a TUR. Adapters that create
// durable holds should implement this; nil / missing cleanup is a no-op for
// admission stubs used in non-authoritative tests.
type BillingAdmissionCleanup interface {
	ReleaseUnused(context.Context, BillingAdmissionInput) error
}

func (e *Executor) authorizeBillingOnce(ctx context.Context, prep *preparedRequest, plan *routePlanState) error {
	if e == nil {
		return nil
	}
	// BillingAuthoritative is a real runtime gate: cutover requires the
	// BillingAdmission port so Bun holds remain the sole monetary admission path.
	if e.BillingAuthoritative && e.BillingAdmission == nil {
		return fmt.Errorf("%w: authoritative billing requires BillingAdmission", ErrBillingAdmissionDenied)
	}
	if e.BillingAdmission == nil {
		// Non-authoritative composition may wire TUR handoff and identity
		// resolvers without an admission adapter. Stamp from resolvers so
		// terminal seal is not skipped; do not invent a hold.
		if e.BillingTerminalHandoff != nil && prep != nil {
			e.stampBillingIdentity(ctx, prep, billing.Authorization{})
		}
		return nil
	}
	if prep == nil || plan == nil {
		return fmt.Errorf("%w: missing prepared route plan", ErrBillingAdmissionDenied)
	}
	hold, err := e.BillingAdmission.Authorize(ctx, e.billingAdmissionInput(prep, plan))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBillingAdmissionDenied, err)
	}
	e.stampBillingIdentity(ctx, prep, hold)
	return nil
}

func (e *Executor) stampBillingIdentity(ctx context.Context, prep *preparedRequest, hold billing.Authorization) {
	if e == nil || prep == nil {
		return
	}
	accountID := strings.TrimSpace(hold.AccountID)
	authID := strings.TrimSpace(hold.ID)
	customerPricing := hold.PricingRef
	chargePolicy := hold.ChargePolicyRef
	// Identity resolvers run only here, once, after successful Authorize.
	// Terminal handoff and abort cleanup read the stamp and never re-resolve.
	if accountID == "" && e.BillingIdentity.AccountID != nil {
		accountID = strings.TrimSpace(e.BillingIdentity.AccountID(ctx, prep.baseline))
	}
	if authID == "" && e.BillingIdentity.AuthorizationID != nil {
		authID = strings.TrimSpace(e.BillingIdentity.AuthorizationID(ctx, prep.baseline, prep.aLeg.ALegID))
	}
	if customerPricing == (billing.VersionRef{}) && e.BillingIdentity.CustomerPricingRef != nil {
		customerPricing = e.BillingIdentity.CustomerPricingRef(ctx, prep.baseline)
	}
	if chargePolicy == (billing.VersionRef{}) && e.BillingIdentity.ChargePolicyRef != nil {
		chargePolicy = e.BillingIdentity.ChargePolicyRef(ctx, prep.baseline)
	}
	if accountID == "" || authID == "" {
		return
	}
	prep.billingAccountID = accountID
	prep.billingAuthorizationID = authID
	prep.billingCustomerPricing = customerPricing
	prep.billingChargePolicy = chargePolicy
	prep.billingIdentityStamped = true
}

func (e *Executor) billingAdmissionInput(prep *preparedRequest, plan *routePlanState) BillingAdmissionInput {
	if prep == nil || plan == nil {
		return BillingAdmissionInput{}
	}
	return BillingAdmissionInput{
		Call: lipapi.CloneCall(prep.baseline), TraceID: prep.traceID, ALegID: prep.aLeg.ALegID,
		Route: plan.sel, RequestSize: plan.requestSize,
	}
}

// releaseOrHandoffAfterAdmissionAbort cleans up after authorize succeeded but
// Execute failed before a request-terminal owner can seal a TUR. Shared evidence
// or a successful backend Open means provider work may have occurred — force a
// detached TUR handoff (or retain the hold when handoff is unwired). Otherwise
// release the unused hold so short-lived pre-open failures cannot park spendable
// balance.
func (e *Executor) releaseOrHandoffAfterAdmissionAbort(ctx context.Context, prep *preparedRequest, plan *routePlanState) {
	if e == nil || prep == nil {
		return
	}
	aLegID := strings.TrimSpace(prep.aLeg.ALegID)
	if aLegID == "" {
		return
	}
	if len(e.peekBillingEvidence(aLegID)) > 0 || prep.billingUpstreamOpened.Load() {
		e.scheduleAbortBillingHandoff(ctx, prep, aLegID)
		return
	}
	cleanup, ok := e.BillingAdmission.(BillingAdmissionCleanup)
	if !ok || cleanup == nil {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := cleanup.ReleaseUnused(releaseCtx, e.billingAdmissionInput(prep, plan)); err != nil && e.Log != nil {
		e.Log.Debug("billing unused-hold release after pre-open abort failed", "a_leg_id", aLegID, "error", err)
	}
}

func (e *Executor) scheduleAbortBillingHandoff(_ context.Context, prep *preparedRequest, aLegID string) {
	if e.BillingTerminalHandoff == nil || prep == nil || !prep.billingIdentityStamped {
		return
	}
	accountID := strings.TrimSpace(prep.billingAccountID)
	authID := strings.TrimSpace(prep.billingAuthorizationID)
	if accountID == "" || authID == "" {
		return
	}
	customerPricing := prep.billingCustomerPricing
	chargePolicy := prep.billingChargePolicy
	job := billingHandoffRetryJob{
		command:         sdkterminal.CommandPartialError,
		accountID:       accountID,
		authorizationID: authID,
		aLegID:          aLegID,
		sessionID:       strings.TrimSpace(prep.baseline.Session.AuthoritativeSessionID),
		customerPricing: customerPricing,
		chargePolicy:    chargePolicy,
		upstreamOpened:  true,
	}
	e.billingTurns().scheduleRetry(job)
}
