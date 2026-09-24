package billingbinding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	runtimecore "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkbilling "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

var (
	// ErrInvalidBinding identifies a binding the adapter refuses to compose.
	ErrInvalidBinding = errors.New("billingbinding: invalid binding")
	// ErrCreditScreen identifies a failed cheap credit screen translation.
	ErrCreditScreen = errors.New("billingbinding: credit screen failed")
	// ErrAdmission identifies a failed quote/admission translation.
	ErrAdmission = errors.New("billingbinding: exposure admission failed")
	// ErrTerminal identifies a failed terminal handoff translation.
	ErrTerminal = errors.New("billingbinding: terminal handoff failed")
	// ErrLifecycle identifies a binding-owned resource lifecycle failure.
	ErrLifecycle = errors.New("billingbinding: lifecycle failed")
)

const (
	// quoteInputVersion is the public quote contract version used for
	// admission-time quotes.
	quoteInputVersion = 1
	// limitInputTokens bounds provider-bound input size for one admission.
	limitInputTokens = "input_tokens"
	// limitMaxOutputTokens bounds provider output for one admission.
	limitMaxOutputTokens = "max_output_tokens"
	// limitUnitToken is the exact-integer token unit for admission limits.
	limitUnitToken = "token"
)

// Adapter binds one validated public billing.Binding to the internal runtime
// billing chokepoints. It is safe for concurrent use by the executor.
type Adapter struct {
	binding   sdkbilling.Binding
	scope     sdkbilling.BindingScope
	lifecycle sdkbilling.Lifecycle

	mu        sync.Mutex
	started   bool
	closeOnce sync.Once
}

var (
	_ runtimecore.BillingCreditGate        = (*Adapter)(nil)
	_ runtimecore.BillingExposureAdmission = (*Adapter)(nil)
	_ billing.TerminalUsageSink            = (*Adapter)(nil)
)

// NewAdapter validates the binding before publication. It rejects
// invalid, typed-nil, and incomplete bindings; duplicate monetary bindings
// are rejected by sdkbilling.ValidateBindings at the facade.
func NewAdapter(binding sdkbilling.Binding) (*Adapter, error) {
	if err := binding.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidBinding, err)
	}
	return &Adapter{
		binding:   binding,
		scope:     binding.Scope.Clone(),
		lifecycle: binding.Lifecycle.Clone(),
	}, nil
}

// Chokepoints exposes the adapter as the three runtime billing chokepoints
// plus the billing identity. The host composition assigns them onto the same
// production ports as the internal reference composition; the adapter never
// invents an internal store.
func (a *Adapter) Chokepoints() (runtimecore.BillingCreditGate, runtimecore.BillingExposureAdmission, billing.TerminalUsageSink, runtimecore.BillingIdentity) {
	if a == nil {
		return nil, nil, nil, runtimecore.BillingIdentity{}
	}
	return a, a, a, a.Identity()
}

// scopeOf resolves the trusted customer scope from the request context.
func scopeOf(ctx context.Context) (scope.PrincipalScopeView, bool) {
	sc, ok := scope.ScopeFromContext(ctx)
	if !ok || !sc.PrincipalID.IsKnown() {
		return scope.PrincipalScopeView{}, false
	}
	return sc, true
}

// accountOf resolves the billing account from trusted scope only. It never
// inspects provider-shaped request data; an unknown principal fails closed.
func accountOf(sc scope.PrincipalScopeView) string {
	if sc.PrincipalID.IsKnown() {
		return strings.TrimSpace(sc.PrincipalID.String())
	}
	return ""
}

// Check implements the cheap pre-route credit screen by translating the
// account identity into a public credit-screen input. Unknown scope fails
// closed without calling the binding. A deny maps onto the internal denied
// class so the executor classifies it exactly like the reference path. The
// binary gate cannot carry degraded guarantees, so degraded fails closed
// through the existing unavailable class before provider execution; it is
// never ordinary allow. Any other failure stays unavailable.
func (a *Adapter) Check(ctx context.Context, accountID string) error {
	if a == nil {
		return fmt.Errorf("%w: nil adapter", ErrCreditScreen)
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return fmt.Errorf("%w: account identity is required", ErrCreditScreen)
	}
	sc, ok := scopeOf(ctx)
	if !ok {
		return fmt.Errorf("%w: trusted customer scope is required", ErrCreditScreen)
	}
	in := sdkbilling.CreditScreenInput{
		Scope:     sc,
		StoreID:   a.scope.StoreID,
		AccountID: accountID,
		Policy:    a.scope.Policy,
	}
	if err := in.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrCreditScreen, err)
	}
	result, err := a.binding.CreditScreen.Check(ctx, in)
	if err != nil {
		return fmt.Errorf("%w: binding screen: %v", ErrCreditScreen, err)
	}
	if err := result.Validate(); err != nil {
		return fmt.Errorf("%w: invalid screen result: %v", ErrCreditScreen, err)
	}
	// A foreign result is never a decision for the requested account,
	// whatever its allow/deny value.
	if !result.MatchesInput(in) {
		return fmt.Errorf("%w: result store %q account %q for request %q/%q: %w",
			ErrCreditScreen, result.StoreID, result.AccountID, in.StoreID, in.AccountID,
			sdkbilling.ErrCreditResultMismatch)
	}
	switch result.Decision {
	case sdkbilling.CreditAllow:
		return nil
	case sdkbilling.CreditDegraded:
		return fmt.Errorf("%w: binding degraded account %q: %w", ErrCreditScreen, accountID, billing.ErrCreditScreenUnavailable)
	case sdkbilling.CreditDeny:
		return fmt.Errorf("%w: binding denied account %q", billing.ErrCreditScreenDenied, accountID)
	default:
		return fmt.Errorf("%w: unknown decision %q", ErrCreditScreen, result.Decision)
	}
}

// Admit implements atomic exposure admission by quoting the frozen offer
// through the binding quoter and admitting the frozen quote through the
// binding admission port. It exercises the same runtime admission path as
// the reference composition: the executor calls Admit once per route plan.
func (a *Adapter) Admit(ctx context.Context, in runtimecore.BillingExposureAdmissionInput) (billing.CallExposure, error) {
	if a == nil {
		return billing.CallExposure{}, fmt.Errorf("%w: nil adapter", ErrAdmission)
	}
	subject := metering.SubjectRef{
		Kind:          metering.SubjectBillingCall,
		StoreID:       a.scope.StoreID,
		BillingCallID: strings.TrimSpace(in.BillingCallID),
	}
	if err := subject.Validate(); err != nil {
		return billing.CallExposure{}, fmt.Errorf("%w: subject: %v", ErrAdmission, err)
	}
	// The trusted admission scope and credited account gate everything
	// downstream: no quote or admission port runs without customer identity.
	// A context scope that names a different principal than the trusted
	// input scope is a foreign mixing and fails here as well.
	if !in.Scope.PrincipalID.IsKnown() {
		return billing.CallExposure{}, fmt.Errorf("%w: trusted admission scope with known principal required", ErrAdmission)
	}
	if ctxScope, ok := scopeOf(ctx); ok && ctxScope.PrincipalID.String() != in.Scope.PrincipalID.String() {
		return billing.CallExposure{}, fmt.Errorf("%w: admission scope principal %q differs from context principal %q", ErrAdmission, in.Scope.PrincipalID.String(), ctxScope.PrincipalID.String())
	}
	accountID := strings.TrimSpace(in.AccountID)
	if accountID == "" {
		accountID = accountOf(in.Scope)
	}
	if accountID == "" {
		if sc, ok := scopeOf(ctx); ok {
			accountID = accountOf(sc)
		}
	}
	if accountID == "" {
		return billing.CallExposure{}, fmt.Errorf("%w: billing account is required", ErrAdmission)
	}
	limits, err := admissionLimits(in)
	if err != nil {
		return billing.CallExposure{}, err
	}
	quoteIn := economics.QuoteInput{
		Version:         quoteInputVersion,
		Subject:         subject,
		CandidateLimits: limits,
		Tariff:          a.scope.Tariff,
		Policy:          a.scope.Policy,
	}
	if err := quoteIn.Validate(); err != nil {
		return billing.CallExposure{}, fmt.Errorf("%w: quote input: %v", ErrAdmission, err)
	}
	quote, err := a.binding.Quoter.Quote(ctx, quoteIn)
	if err != nil {
		return billing.CallExposure{}, fmt.Errorf("%w: binding quote: %v", ErrAdmission, err)
	}
	if err := quote.Validate(); err != nil {
		return billing.CallExposure{}, fmt.Errorf("%w: invalid quote: %v", ErrAdmission, err)
	}
	// The quote must bind the requested call and the binding's frozen
	// snapshots before atomic admission is invoked. Foreign subjects or
	// snapshots fail here, with no admission port call.
	if err := a.checkQuoteBinding(quote, subject); err != nil {
		return billing.CallExposure{}, err
	}
	admitIn := sdkbilling.ExposureAdmissionInput{
		Subject:         subject,
		Scope:           in.Scope,
		AccountID:       accountID,
		Quote:           quote,
		ExecutionLimits: limits,
	}
	if err := admitIn.Validate(); err != nil {
		return billing.CallExposure{}, fmt.Errorf("%w: admission input: %v", ErrAdmission, err)
	}
	handle, err := a.binding.Admission.Admit(ctx, admitIn)
	if err != nil {
		return billing.CallExposure{}, fmt.Errorf("%w: binding admit: %v", ErrAdmission, err)
	}
	if err := handle.Validate(); err != nil {
		return billing.CallExposure{}, fmt.Errorf("%w: invalid handle: %v", ErrAdmission, err)
	}
	// The handle must authorize exactly the admitted request. A foreign
	// store, call, or quote identity cannot authorize this call.
	if !handle.MatchesAdmission(admitIn) {
		return billing.CallExposure{}, fmt.Errorf("%w: handle store %q call %q quote %q for request %q/%q: %w",
			ErrAdmission, handle.StoreID, handle.BillingCallID, handle.QuoteID,
			admitIn.Subject.StoreID, admitIn.Subject.BillingCallID,
			sdkbilling.ErrAdmissionMismatch)
	}
	max := billing.Money{}
	if quote.Maximum.Present {
		max, err = billing.NewMoney(quote.Maximum.NanoUnits, quote.Maximum.Currency)
		if err != nil {
			return billing.CallExposure{}, fmt.Errorf("%w: quote maximum: %v", ErrAdmission, err)
		}
	}
	// The frozen quote carries the admitted references; a quote that omits
	// them falls back to the binding scope identities the quote was built
	// under. Executor fallbacks resolve to the same scope values.
	pricing := quote.Tariff
	if pricing.ID == "" {
		pricing = a.scope.Tariff
	}
	chargePolicy := quote.Policy
	if chargePolicy.ID == "" {
		chargePolicy = a.scope.Policy
	}
	return billing.CallExposure{
		AccountID:       accountID,
		CallID:          strings.TrimSpace(in.BillingCallID),
		Max:             max,
		PricingRef:      billing.VersionRef{ID: pricing.ID, Version: pricing.Version},
		ChargePolicyRef: billing.VersionRef{ID: chargePolicy.ID, Version: chargePolicy.Version},
	}, nil
}

// checkQuoteBinding requires the quoter response to name exactly the
// requested call subject and the binding's frozen snapshot identities.
// Tariff ID/version and policy ID/version/owner must equal the scope the
// quote was built under; anything else is rejected before admission.
func (a *Adapter) checkQuoteBinding(quote economics.ExposureQuote, subject metering.SubjectRef) error {
	if quote.Subject.Kind != subject.Kind ||
		quote.Subject.StoreID != subject.StoreID ||
		quote.Subject.BillingCallID != subject.BillingCallID {
		return fmt.Errorf("%w: quote subject %q/%q/%q for requested %q/%q/%q: %w",
			ErrAdmission,
			quote.Subject.Kind, quote.Subject.StoreID, quote.Subject.BillingCallID,
			subject.Kind, subject.StoreID, subject.BillingCallID,
			sdkbilling.ErrQuoteMismatch)
	}
	if quote.Tariff.ID != a.scope.Tariff.ID || quote.Tariff.Version != a.scope.Tariff.Version {
		return fmt.Errorf("%w: quote tariff %q/%q differs from frozen %q/%q: %w",
			ErrAdmission, quote.Tariff.ID, quote.Tariff.Version,
			a.scope.Tariff.ID, a.scope.Tariff.Version,
			sdkbilling.ErrQuoteMismatch)
	}
	if quote.Policy.ID != a.scope.Policy.ID || quote.Policy.Version != a.scope.Policy.Version ||
		quote.Policy.PolicyID != a.scope.Policy.PolicyID {
		return fmt.Errorf("%w: quote policy %q/%q/%q differs from frozen %q/%q/%q: %w",
			ErrAdmission, quote.Policy.ID, quote.Policy.Version, quote.Policy.PolicyID,
			a.scope.Policy.ID, a.scope.Policy.Version, a.scope.Policy.PolicyID,
			sdkbilling.ErrQuoteMismatch)
	}
	return nil
}

// admissionLimits projects bounded internal size facts onto finite public
// execution limits. Strict offers with no defensible bound fail closed here,
// before provider execution, matching the reference admission semantics.
func admissionLimits(in runtimecore.BillingExposureAdmissionInput) ([]economics.Limit, error) {
	var limits []economics.Limit
	if in.RequestSize.Available {
		if in.RequestSize.Tokens < 0 {
			return nil, fmt.Errorf("%w: negative request size", ErrAdmission)
		}
		limits = append(limits, economics.Limit{Name: limitInputTokens, Value: in.RequestSize.Tokens, Unit: limitUnitToken})
	}
	if in.MaxOutputTokens != nil {
		if *in.MaxOutputTokens < 0 {
			return nil, fmt.Errorf("%w: negative max output", ErrAdmission)
		}
		limits = append(limits, economics.Limit{Name: limitMaxOutputTokens, Value: int64(*in.MaxOutputTokens), Unit: limitUnitToken})
	}
	if len(limits) == 0 {
		return nil, fmt.Errorf("%w: at least one finite execution limit is required", ErrAdmission)
	}
	return limits, nil
}

// AppendLeg implements the terminal B-leg handoff by translating the leg
// record — including its neutral V2 observations — into a public terminal
// envelope. The binding durably acknowledges; success is returned only for a
// valid acknowledgement.
func (a *Adapter) AppendLeg(ctx context.Context, record billing.CallLegUsageRecord) error {
	if a == nil {
		return fmt.Errorf("%w: nil adapter", ErrTerminal)
	}
	outcome, err := legOutcomeToTerminal(record.Outcome)
	if err != nil {
		return err
	}
	if record.AttemptSeq <= 0 {
		return fmt.Errorf("%w: b-leg closure requires a positive authoritative attempt sequence", ErrTerminal)
	}
	callID := strings.TrimSpace(record.CallID.String())
	if callID == "" {
		return fmt.Errorf("%w: billing call identity is required", ErrTerminal)
	}
	bLegID := strings.TrimSpace(record.BLegID)
	if bLegID == "" {
		return fmt.Errorf("%w: B-leg identity is required", ErrTerminal)
	}
	envelopeID := strings.TrimSpace(record.Key)
	if envelopeID == "" {
		envelopeID, err = billing.CallLegUsageKey(record.CallID, bLegID)
		if err != nil {
			return fmt.Errorf("%w: envelope identity: %v", ErrTerminal, err)
		}
	}
	seq := uint64(record.AttemptSeq)
	aLegID := strings.TrimSpace(record.ALegID)
	submissionID := strings.TrimSpace(record.SubmissionID)
	// Terminal scope is mandatory, never best-effort: the runtime freezes
	// request scope onto every handoff context, so a missing scope means
	// unattributable evidence. The sink is not invoked.
	sc, ok := scopeOf(ctx)
	if !ok {
		return fmt.Errorf("%w: trusted terminal scope is required", ErrTerminal)
	}
	env := sdkbilling.TerminalEnvelope{
		Version:    sdkbilling.TerminalEnvelopeVersionV1,
		EnvelopeID: envelopeID,
		StoreID:    a.scope.StoreID,
		Scope:      sc,
		Subject: metering.SubjectRef{
			Kind:          metering.SubjectBLeg,
			StoreID:       a.scope.StoreID,
			BillingCallID: callID,
			BLegID:        bLegID,
			AttemptSeq:    seq,
			ALegID:        aLegID,
			SubmissionID:  submissionID,
		},
		Correlation: metering.CorrelationV2{
			StoreID:       a.scope.StoreID,
			BillingCallID: callID,
			BLegID:        bLegID,
			AttemptSeq:    seq,
			ALegID:        aLegID,
			SubmissionID:  submissionID,
		},
		Outcome:      outcome,
		AttemptSeq:   seq,
		ALegID:       aLegID,
		SubmissionID: submissionID,
		Workload:     mapWorkload(record.Workload),
		Tariff:       a.scope.Tariff,
		Policy:       a.scope.Policy,
		Observations: append([]metering.Observation(nil), record.Observations...),
		PayloadHash:  terminalPayloadHash(strings.TrimSpace(record.Key), strings.TrimSpace(record.Fingerprint)),
	}
	return a.appendEnvelope(ctx, env)
}

// AppendCall implements the terminal call-closure handoff. Call closures
// carry identity, outcome, and frozen references; request-scoped inference
// evidence travels on B-leg envelopes.
func (a *Adapter) AppendCall(ctx context.Context, record billing.CallUsageRecord) error {
	if a == nil {
		return fmt.Errorf("%w: nil adapter", ErrTerminal)
	}
	outcome, err := turnOutcomeToTerminal(record.Outcome)
	if err != nil {
		return err
	}
	callID := strings.TrimSpace(record.CallID.String())
	if callID == "" {
		return fmt.Errorf("%w: billing call identity is required", ErrTerminal)
	}
	envelopeID := strings.TrimSpace(record.Key)
	if envelopeID == "" {
		envelopeID, err = billing.CallUsageKey(record.CallID)
		if err != nil {
			return fmt.Errorf("%w: envelope identity: %v", ErrTerminal, err)
		}
	}
	aLegID := strings.TrimSpace(record.ALegID)
	submissionID := strings.TrimSpace(record.SubmissionID)
	// Terminal scope is mandatory, never best-effort: the runtime freezes
	// request scope onto every handoff context, so a missing scope means
	// unattributable evidence. The sink is not invoked.
	sc, ok := scopeOf(ctx)
	if !ok {
		return fmt.Errorf("%w: trusted terminal scope is required", ErrTerminal)
	}
	env := sdkbilling.TerminalEnvelope{
		Version:    sdkbilling.TerminalEnvelopeVersionV1,
		EnvelopeID: envelopeID,
		StoreID:    a.scope.StoreID,
		Scope:      sc,
		Subject: metering.SubjectRef{
			Kind:          metering.SubjectBillingCall,
			StoreID:       a.scope.StoreID,
			BillingCallID: callID,
			ALegID:        aLegID,
			SubmissionID:  submissionID,
		},
		Correlation: metering.CorrelationV2{
			StoreID:       a.scope.StoreID,
			BillingCallID: callID,
			ALegID:        aLegID,
			SubmissionID:  submissionID,
		},
		Outcome:         outcome,
		AccountID:       strings.TrimSpace(record.AccountID),
		ALegID:          aLegID,
		SubmissionID:    submissionID,
		Workload:        mapWorkload(record.Workload),
		ExpectedBLegIDs: append([]string(nil), record.ExpectedBLegIDs...),
		Tariff:          a.scope.Tariff,
		Policy:          a.scope.Policy,
		PayloadHash:     terminalPayloadHash(strings.TrimSpace(record.Key), strings.TrimSpace(record.Fingerprint)),
	}
	return a.appendEnvelope(ctx, env)
}

// mapWorkload projects internal workload correlation onto the public
// envelope. A zero workload maps to nil: the closure carries no workload
// attribution rather than an empty label.
func mapWorkload(workload billing.WorkloadIdentity) *sdkbilling.TerminalWorkload {
	if workload.Class == "" && workload.Role == "" {
		return nil
	}
	return &sdkbilling.TerminalWorkload{
		Class: string(workload.Class),
		Role:  string(workload.Role),
	}
}

// appendEnvelope validates the translated envelope, hands it to the binding,
// and validates the durable acknowledgement. The acknowledgement must answer
// exactly the sent envelope: a foreign store or envelope identity is a typed
// mismatch, never durable success. A returned error means the record must be
// retried and is never silently dropped.
func (a *Adapter) appendEnvelope(ctx context.Context, env sdkbilling.TerminalEnvelope) error {
	if err := env.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrTerminal, err)
	}
	ack, err := a.binding.Terminal.AppendTerminal(ctx, env)
	if err != nil {
		return fmt.Errorf("%w: binding terminal: %v", ErrTerminal, err)
	}
	if err := ack.Validate(); err != nil {
		return fmt.Errorf("%w: invalid acknowledgement: %v", ErrTerminal, err)
	}
	if !ack.MatchesEnvelope(env) {
		return fmt.Errorf("%w: ack store %q envelope %q for envelope %q/%q: %w",
			ErrTerminal, ack.StoreID, ack.EnvelopeID, env.StoreID, env.EnvelopeID,
			sdkbilling.ErrTerminalAckMismatch)
	}
	return nil
}

// terminalPayloadHash binds the envelope to the exact internal record
// identity and fingerprint with a stable hex digest.
func terminalPayloadHash(key, fingerprint string) string {
	sum := sha256.Sum256([]byte(key + "\x00" + fingerprint))
	return hex.EncodeToString(sum[:])
}

// legOutcomeToTerminal maps terminal B-leg outcomes onto the public closure
// vocabulary. Non-terminal dispositions (rejected, never started, unknown)
// have no honest terminal mapping and fail closed.
func legOutcomeToTerminal(outcome billing.LegOutcome) (sdkbilling.TerminalOutcome, error) {
	switch outcome {
	case billing.LegOutcomeWinner, billing.LegOutcomeLoser:
		return sdkbilling.TerminalCompleted, nil
	case billing.LegOutcomeFailed, billing.LegOutcomeSwallowed:
		return sdkbilling.TerminalFailed, nil
	case billing.LegOutcomeCanceled:
		return sdkbilling.TerminalCanceled, nil
	default:
		return "", fmt.Errorf("%w: unmappable leg outcome %q", ErrTerminal, outcome)
	}
}

// turnOutcomeToTerminal maps terminal call outcomes onto the public closure
// vocabulary. Unknown outcomes fail closed rather than inventing finality.
func turnOutcomeToTerminal(outcome billing.TurnOutcome) (sdkbilling.TerminalOutcome, error) {
	switch outcome {
	case billing.TurnOutcomeCompleted:
		return sdkbilling.TerminalCompleted, nil
	case billing.TurnOutcomeFailed:
		return sdkbilling.TerminalFailed, nil
	case billing.TurnOutcomeCanceled:
		return sdkbilling.TerminalCanceled, nil
	default:
		return "", fmt.Errorf("%w: unmappable call outcome %q", ErrTerminal, outcome)
	}
}

// Identity resolves runtime billing identity from the binding scope and
// trusted per-request scope. It is wire-bounded: resolvers read only scope
// and static snapshot identities, never provider-shaped request data, so the
// large-payload fast path stays open.
func (a *Adapter) Identity() runtimecore.BillingIdentity {
	bound := a.scope
	storeID := bound.StoreID
	pricing := billing.VersionRef{ID: bound.Tariff.ID, Version: bound.Tariff.Version}
	policy := billing.VersionRef{ID: bound.Policy.ID, Version: bound.Policy.Version}
	account := func(ctx context.Context, _ lipapi.Call) string {
		sc, ok := scopeOf(ctx)
		if !ok {
			return ""
		}
		return accountOf(sc)
	}
	return runtimecore.BillingIdentity{
		AccountID:              account,
		StoreID:                func(context.Context) string { return storeID },
		CustomerPricingRef:     func(context.Context, lipapi.Call) billing.VersionRef { return pricing },
		ChargePolicyRef:        func(context.Context, lipapi.Call) billing.VersionRef { return policy },
		OperatorRateRef:        func(context.Context, string, string) billing.VersionRef { return pricing },
		WireBounded:            true,
		WireAccountID:          func(_ context.Context, sc scope.PrincipalScopeView) string { return accountOf(sc) },
		WireCustomerPricingRef: func(context.Context) billing.VersionRef { return pricing },
		WireChargePolicyRef:    func(context.Context) billing.VersionRef { return policy },
	}
}

// StartOwnedResources starts binding-owned resources once, in registration
// order. A start failure unwinds already-started resources in reverse order
// and leaves the adapter unstarted so the build fails before publication.
func (a *Adapter) StartOwnedResources(ctx context.Context) error {
	if a == nil {
		return fmt.Errorf("%w: nil adapter", ErrLifecycle)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return nil
	}
	var started []sdkbilling.OwnedResource
	for _, resource := range a.lifecycle.Owned {
		if err := resource.Start(ctx); err != nil {
			if unwind := closeOwnedReverse(context.Background(), started); unwind != nil {
				return fmt.Errorf("%w: start %q: %v; unwind: %v", ErrLifecycle, resource.ID, err, unwind)
			}
			return fmt.Errorf("%w: start %q: %v", ErrLifecycle, resource.ID, err)
		}
		started = append(started, resource)
	}
	a.started = true
	return nil
}

// Close releases started binding-owned resources once, in reverse start
// order. Borrowed handles are never touched. It is registered with host
// cleanup exactly once, so normal Host/Manager Close and repeated Close
// dispose owned resources exactly once.
func (a *Adapter) Close() error {
	if a == nil {
		return nil
	}
	var err error
	a.closeOnce.Do(func() {
		a.mu.Lock()
		started := a.started
		owned := append([]sdkbilling.OwnedResource(nil), a.lifecycle.Owned...)
		a.mu.Unlock()
		if !started {
			return
		}
		err = closeOwnedReverse(context.Background(), owned)
	})
	return err
}

// closeOwnedReverse closes owned resources in reverse order, joining errors.
func closeOwnedReverse(ctx context.Context, owned []sdkbilling.OwnedResource) error {
	var out error
	for i := len(owned) - 1; i >= 0; i-- {
		if err := owned[i].Close(ctx); err != nil {
			out = errors.Join(out, fmt.Errorf("%w: close %q: %v", ErrLifecycle, owned[i].ID, err))
		}
	}
	return out
}
