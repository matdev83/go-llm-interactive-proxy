package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

var (
	_ largebody.LargeBodyExecutor                  = (*Executor)(nil)
	_ largebody.LargeBodyWireExecutor              = (*Executor)(nil)
	_ largebody.LargeBodyStaticDispositionProvider = (*Executor)(nil)
)

// wireLifecycleEventStream wraps an EventStream to release request authority
// and execute terminal lifecycle and economic cleanup once when the stream
// is closed, consumed to EOF, or canceled.
type wireLifecycleEventStream struct {
	lipapi.EventStream
	cleanup func(err error)
	mu      sync.Mutex
	lastErr error
	once    sync.Once
}

var _ lipapi.ManagedEventStream = (*wireLifecycleEventStream)(nil)

func (s *wireLifecycleEventStream) lastRecvErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func (s *wireLifecycleEventStream) noteRecvErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = err
}

func (s *wireLifecycleEventStream) Close() error {
	var err error
	if s.EventStream != nil {
		err = s.EventStream.Close()
	}
	// A close with no prior Recv error is an early close before EOF, which
	// canonical semantics classify as canceled (CommandClose), never Winner.
	cause := s.lastRecvErr()
	if cause == nil {
		cause = context.Canceled
	}
	s.once.Do(func() {
		if s.cleanup != nil {
			s.cleanup(cause)
		}
	})
	return err
}

func (s *wireLifecycleEventStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.EventStream == nil {
		s.once.Do(func() {
			if s.cleanup != nil {
				s.cleanup(io.EOF)
			}
		})
		return lipapi.Event{}, io.EOF
	}
	ev, err := s.EventStream.Recv(ctx)
	if err != nil {
		s.noteRecvErr(err)
		s.once.Do(func() {
			if s.cleanup != nil {
				s.cleanup(err)
			}
		})
	}
	return ev, err
}

func (s *wireLifecycleEventStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	s.once.Do(func() {
		if s.cleanup != nil {
			s.cleanup(context.Canceled)
		}
	})
	if ms, ok := s.EventStream.(lipapi.ManagedEventStream); ok {
		return ms.Cancel(ctx, cause)
	}
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

// AssessLargeBody evaluates frontend proof for candidate fast-path execution (Phase 4).
// If LargeBodyAssessor is not configured, it returns a declined assessment (fail closed to canonical).
func (e *Executor) AssessLargeBody(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	if e == nil || e.LargeBodyAssessor == nil {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonAuthorityBlocker)
		return dec, nil
	}
	return e.LargeBodyAssessor.AssessLargeBody(ctx, proof)
}

// LargeBodyStaticDisposition returns an O(1) static wire disposition for a profile (Phase 4).
// If LargeBodyAssessor implements LargeBodyStaticDispositionProvider, it delegates to it.
// Otherwise, it returns DefinitelyCanonical with StaticBlocker.
func (e *Executor) LargeBodyStaticDisposition(profileID string) (largebody.StaticWireDisposition, largebody.StaticWireReason) {
	if e == nil || e.LargeBodyAssessor == nil {
		return largebody.StaticWireDefinitelyCanonical, largebody.StaticWireReasonStaticBlocker
	}
	if sdp, ok := e.LargeBodyAssessor.(largebody.LargeBodyStaticDispositionProvider); ok {
		return sdp.LargeBodyStaticDisposition(profileID)
	}
	return largebody.StaticWireNeedsRequestAssessment, largebody.StaticWireReasonNone
}

// LaneDomainPolicy configures late-route domain constraints per frontend lane.
type LaneDomainPolicy struct {
	// UniversalOnly requires backend universal model certification (AnyAcceptedModel: true)
	// and evaluates universal late-route domain proof (e.g. Lane 3 OpenResponses).
	UniversalOnly bool
	// CandidateModels lists finite candidate models certified for the lane when not UniversalOnly.
	CandidateModels []string
}

// StandardLaneDomainPolicies returns the default domain policies for standard frontend lanes.
func StandardLaneDomainPolicies() map[string]LaneDomainPolicy {
	return map[string]LaneDomainPolicy{
		"openai_responses_v1": {UniversalOnly: false},
		"openai_chat_v1":      {UniversalOnly: false},
		"openresponses_v1":    {UniversalOnly: true},
	}
}

// ProductionLargeBodyAssessor unifies authority assessment, backend wire proof,
// and route override/late selector gates into a side-effect-free proof assessor
// for runtime.Executor (Phase 4, Requirements 5, 6, 7, 8, 9, 13, 14, 15, 19).
type ProductionLargeBodyAssessor struct {
	GenerationID              string
	CandidateDomainGeneration string
	AuthorityGate             *largebody.AuthorityAssessmentGate
	WireProofGate             *largebody.BackendWireProofGate
	LaneDomainPolicies        map[string]LaneDomainPolicy
}

var _ largebody.LargeBodyAssessor = (*ProductionLargeBodyAssessor)(nil)
var _ largebody.LargeBodyStaticDispositionProvider = (*ProductionLargeBodyAssessor)(nil)

// NewProductionLargeBodyAssessor constructs a ProductionLargeBodyAssessor.
func NewProductionLargeBodyAssessor(
	generationID string,
	candidateDomainGen string,
	authGate *largebody.AuthorityAssessmentGate,
	wireProofGate *largebody.BackendWireProofGate,
	lanePolicies map[string]LaneDomainPolicy,
) *ProductionLargeBodyAssessor {
	return &ProductionLargeBodyAssessor{
		GenerationID:              generationID,
		CandidateDomainGeneration: candidateDomainGen,
		AuthorityGate:             authGate,
		WireProofGate:             wireProofGate,
		LaneDomainPolicies:        lanePolicies,
	}
}

// AssessLargeBody evaluates frontend proof across authority and wire proof gates.
// Invariants:
//   - Streaming-only delivery gate (proof.Delivery == DeliveryModeStreaming; 15.4 carry).
//   - Universal-vs-finite domain policy per lane.
//   - Same-permit decline discipline: always returns (declined, nil) on decline, never an error,
//     so caller continues canonical processing under held decode permit.
func (a *ProductionLargeBodyAssessor) AssessLargeBody(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	if ctx != nil && ctx.Err() != nil {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonCanceled)
		return dec, nil
	}
	if strings.TrimSpace(proof.ProfileID) == "" {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonProofUncertain)
		return dec, nil
	}
	if proof.Delivery != lipapi.DeliveryModeStreaming {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonAuthorityBlocker)
		return dec, nil
	}
	if a == nil || a.AuthorityGate == nil {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonAuthorityBlocker)
		return dec, nil
	}

	authDecision, authReason := a.AuthorityGate.Evaluate()
	if authDecision == largebody.AssessmentDecisionDecline {
		dec, _ := largebody.NewDeclinedAssessment(authReason)
		return dec, nil
	}

	if a.WireProofGate == nil {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonAuthorityBlocker)
		return dec, nil
	}

	wpGate := a.WireProofGate
	if a.LaneDomainPolicies != nil {
		if pol, ok := a.LaneDomainPolicies[proof.ProfileID]; ok && pol.UniversalOnly {
			if wpGate.OverrideGate != nil && len(wpGate.OverrideGate.CandidateModels) > 0 {
				cloned := *wpGate
				ovClone := *wpGate.OverrideGate
				ovClone.CandidateModels = nil
				cloned.OverrideGate = &ovClone
				wpGate = &cloned
			}
		}
	}

	wpDecision, wpReason, wireReq, wireDomain, cands := wpGate.Evaluate(ctx, proof)
	if wpDecision == largebody.AssessmentDecisionDecline {
		dec, _ := largebody.NewDeclinedAssessment(wpReason)
		return dec, nil
	}

	if pol, ok := a.LaneDomainPolicies[proof.ProfileID]; ok && pol.UniversalOnly {
		domainFacts := largebody.WireDomainFacts{
			ProfileID:      proof.ProfileID,
			Operation:      proof.Operation,
			Delivery:       proof.Delivery,
			BodyMode:       proof.Mode,
			Rewrite:        proof.Rewrite,
			UniversalModel: true,
		}
		for _, cand := range cands {
			backendID := strings.TrimSpace(cand.Primary.Backend)
			var wb largebody.WireBackend
			var ok bool
			if wpGate.BackendResolver != nil {
				wb, ok = wpGate.BackendResolver.ResolveWireBackend(backendID)
			}
			if !ok || wb == nil {
				dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonBackendIncompatible)
				return dec, nil
			}
			support := wb.ResolveWireDomain(ctx, domainFacts)
			if !support.Compatible || !support.AnyAcceptedModel {
				dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonBackendIncompatible)
				return dec, nil
			}
		}
		wireDomain = domainFacts
	}

	cGen := a.CandidateDomainGeneration
	if cGen == "" {
		cGen = a.GenerationID
	}
	stamp, err := largebody.BindAssessmentStamp(a.GenerationID, proof, cGen)
	if err != nil {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonProofUncertain)
		return dec, nil
	}

	accepted, err := largebody.NewAcceptedAssessment(stamp, wireReq, wireDomain)
	if err != nil {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonProofUncertain)
		return dec, nil
	}
	return accepted, nil
}

// LargeBodyStaticDisposition returns an O(1) static wire disposition for a profile.
func (a *ProductionLargeBodyAssessor) LargeBodyStaticDisposition(profileID string) (largebody.StaticWireDisposition, largebody.StaticWireReason) {
	if a == nil || a.AuthorityGate == nil {
		return largebody.StaticWireDefinitelyCanonical, largebody.StaticWireReasonStaticBlocker
	}
	if a.AuthorityGate.Summary.HasStaticBlocker() {
		return largebody.StaticWireDefinitelyCanonical, largebody.StaticWireReasonStaticBlocker
	}
	if a.WireProofGate == nil || a.WireProofGate.BackendResolver == nil {
		return largebody.StaticWireDefinitelyCanonical, largebody.StaticWireReasonStaticBlocker
	}
	return largebody.StaticWireNeedsRequestAssessment, largebody.StaticWireReasonNone
}

// ExecuteLargeBody implements largebody.LargeBodyWireExecutor (Requirements 6, 7, 14, 15, 18, 19; Task 13.1).
// It crosses the one-way wire commit barrier, runs the wire secure-session preparation and exactly one
// BeginTurn/A-leg lifecycle, reads the post-BeginTurn live route override constrained to the assessed domain,
// applies request authority and economic admission, and returns an ExecutionResult containing the canonical
// EventStream, bounded ResponseFacts, and the sensitive SessionResponseCarrier.
// Under no circumstance does this method fall back to canonical Execute.
func (e *Executor) ExecuteLargeBody(
	ctx context.Context,
	accepted largebody.Assessment,
	src largebody.Source,
) (largebody.ExecutionResult, error) {
	turnFinished := false
	defer func() {
		if !turnFinished && src != nil {
			_ = src.Close()
		}
	}()

	if ctx != nil && ctx.Err() != nil {
		return largebody.ExecutionResult{}, ctx.Err()
	}

	// 1. Validate assessment stamp and source ownership (Requirements 6.6, 6.7, 8.5)
	if src == nil {
		return largebody.ExecutionResult{}, fmt.Errorf("executor: largebody source must not be nil (invariant failure)")
	}

	var srcDigest largebody.SourceDigest
	if digester, ok := src.(interface{ Digest() largebody.SourceDigest }); ok {
		srcDigest = digester.Digest()
	} else if digester, ok := src.(interface{ SourceDigest() largebody.SourceDigest }); ok {
		srcDigest = digester.SourceDigest()
	} else {
		srcDigest = accepted.Stamp.SourceDigest()
	}

	genID := e.LargeBodyGenerationID
	if genID == "" {
		genID = accepted.Stamp.GenerationID()
	}
	cGen := e.LargeBodyCandidateDomainGeneration
	if cGen == "" {
		cGen = accepted.Stamp.CandidateDomainGeneration()
	}

	live := largebody.LiveExecutionFacts{
		GenerationID:              genID,
		ProfileID:                 accepted.Stamp.ProfileID(),
		Source:                    srcDigest,
		BodyBytes:                 src.Size(),
		Mode:                      accepted.Stamp.BodyMode(),
		Rewrite:                   accepted.Stamp.Rewrite(),
		CandidateDomainGeneration: cGen,
	}

	if err := largebody.ValidateExecuteLargeBody(accepted, src, live); err != nil {
		return largebody.ExecutionResult{}, err
	}

	// 2. Resolve wire turn facts
	turnFacts := e.resolveWireTurnFacts(ctx, accepted, src, srcDigest)

	traceID := strings.TrimSpace(turnFacts.Identity.TraceID)
	if traceID == "" {
		traceID = strings.TrimSpace(turnFacts.Identity.RequestID)
	}
	if traceID == "" {
		traceID = "wire-trace"
	}
	requestID := strings.TrimSpace(turnFacts.Identity.RequestID)
	if requestID == "" {
		requestID = traceID
	}

	// 3. Wire secure-session preparation (Requirements 6.2, 14.1)
	prep, err := e.PrepareSecureSession(ctx, SecureSessionPrepInput{
		TraceID: traceID,
		Session: turnFacts.Session.Input,
	})
	if err != nil {
		return largebody.ExecutionResult{}, err
	}
	outCtx := prep.Context()

	// 4. One-way commit barrier: ExecuteBeginTurn (Requirements 6.5, 6.6, 14.1)
	br, err := prep.ExecuteBeginTurn(outCtx)
	if err != nil {
		return largebody.ExecutionResult{}, err
	}
	outCtx = execctx.WithSecureSessionTurn(outCtx, prep.SecureTurn(br))

	defer func() {
		if !turnFinished && e.SecureSession != nil {
			_ = e.SecureSession.FinishTurn(context.WithoutCancel(outCtx), br.Record.SessionID, br.TurnID, app.TurnOutcome{
				Kind: app.TurnOutcomeSurfacedFailure,
			})
		}
	}()

	// 5. Resolve A-leg & snapshot route override (Requirements 7.4, 14.1)
	aLeg, routeAuth, err := prep.ResolveALeg(outCtx, br.Record.ALegID)
	if err != nil {
		return largebody.ExecutionResult{}, err
	}

	// Bind session view and record client turn shape if present
	_ = prep.BindSession(br, aLeg)
	if len(turnFacts.Session.TurnShape.Items) > 0 {
		if rerr := prep.RecordClientTurnWithShape(outCtx, br, turnFacts.Session.TurnShape, largebody.DefaultMaxSemanticFactBytes); rerr != nil {
			if e.SecureSessionRecordingMandatory {
				return largebody.ExecutionResult{}, rerr
			}
		}
	}

	// 6. Read live route override only now, constrained to assessed domain (Requirement 7)
	effectiveModel := turnFacts.Route.CandidateModel
	if effectiveModel == "" {
		effectiveModel = turnFacts.Route.ClientModel
	}
	if effectiveModel == "" {
		effectiveModel = accepted.WireRequest.CandidateModel
	}
	if effectiveModel == "" {
		effectiveModel = accepted.WireRequest.ClientModel
	}

	if routeAuth.active() {
		overrideSel := strings.TrimSpace(routeAuth.State.Selector)
		if overrideSel != "" {
			if accepted.WireDomain.UniversalModel {
				effectiveModel = overrideSel
			} else {
				matched := false
				for _, m := range accepted.WireDomain.CandidateModels {
					if m == overrideSel {
						matched = true
						break
					}
				}
				if !matched && e.SelectorAliases != nil {
					resolved := e.SelectorAliases.Resolve(overrideSel)
					for _, m := range accepted.WireDomain.CandidateModels {
						if m == resolved {
							matched = true
							overrideSel = resolved
							break
						}
					}
				}
				if !matched {
					return largebody.ExecutionResult{}, fmt.Errorf("executor: route override selector %q is outside assessed wire domain", overrideSel)
				}
				effectiveModel = overrideSel
			}
		}
	}

	// 7. Frontend ingress checkpoint capture (Requirements 15.1–15.3, 19)
	var maxOutputTokens *int
	if turnFacts.MaxOutput.MaxOutputTokens > 0 {
		v := int(turnFacts.MaxOutput.MaxOutputTokens)
		maxOutputTokens = &v
	}
	outCtx, _, err = prep.CaptureFrontendIngressCheckpoint(outCtx, requestID, br, aLeg, maxOutputTokens)
	if err != nil {
		return largebody.ExecutionResult{}, err
	}

	// 8. Request authority admission (Requirements 4.5, 8.1, 10.4)
	outCtx, err = e.admitRequestAuthorityOnce(outCtx, requestID, aLeg.ALegID, traceID, prep.Scope())
	if err != nil {
		return largebody.ExecutionResult{}, err
	}

	// Ensure request authority is released if subsequent economic admission steps fail
	committed := false
	defer func() {
		if !committed {
			_ = e.releaseRequestAuthority(outCtx)
		}
	}()

	// 9. Economic admission (Requirements 15.6, 19)
	if err := e.CheckWireCheapCredit(outCtx, WireBillingCreditArgs{
		Scope: prep.Scope(),
	}); err != nil {
		return largebody.ExecutionResult{}, err
	}

	var billingCallID billing.BillingCallID
	if parsed, perr := billing.ParseBillingCallID(turnFacts.Economic.BillingCallID); perr == nil {
		billingCallID = parsed
	} else {
		newID, nerr := billing.NewBillingCallID()
		if nerr != nil {
			return largebody.ExecutionResult{}, fmt.Errorf("executor: new billing call id: %w", nerr)
		}
		billingCallID = newID
	}
	turnFacts.Economic.BillingCallID = billingCallID.String()

	primary := routing.Primary{Model: effectiveModel}
	sel := &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}}

	callExposure, err := e.AuthorizeWireBilling(outCtx, WireBillingExposureArgs{
		BillingCallID:   billingCallID,
		TraceID:         traceID,
		ALegID:          aLeg.ALegID,
		SessionID:       string(br.Record.SessionID),
		Scope:           prep.Scope(),
		Route:           sel,
		RequestSize:     routing.RequestSizeEstimate{Available: true, Tokens: turnFacts.Source.BodyBytes, Basis: "wire_body_bytes"},
		MaxOutputTokens: maxOutputTokens,
	})
	if err != nil {
		return largebody.ExecutionResult{}, err
	}

	billingState := newBillingCallState(billingCallID)

	// 10. Attempt execution under existing attempt ownership (Requirements 8, 9, 10, 12, 15)
	aScope := e.lifecycleCoordinator().StartALeg(aLeg.ALegID)
	attemptOut, err := e.executeWireAttempts(wireAttemptInput{
		ctx:                   ctx,
		outCtx:                outCtx,
		accepted:              accepted,
		src:                   src,
		turnFacts:             turnFacts,
		traceID:               traceID,
		requestID:             requestID,
		aLegID:                aLeg.ALegID,
		sessionID:             string(br.Record.SessionID),
		effectiveModel:        effectiveModel,
		maxOutputTokens:       maxOutputTokens,
		aScope:                aScope,
		billingState:          billingState,
		weightedFirstConsumed: aLeg.WeightedFirstConsumed,
	})
	if err != nil {
		if aScope != nil {
			aScope.End()
		}
		if callExposure.AccountID != "" {
			_ = e.AppendWireExposureAbort(outCtx, WireExposureAbortArgs{
				BillingCallID:   billingCallID,
				Exposure:        callExposure,
				ALegID:          aLeg.ALegID,
				SessionID:       string(br.Record.SessionID),
				ExpectedBLegIDs: billingState.freezeAllocatedBLegs(),
				Now:             e.now(),
			})
		}
		return largebody.ExecutionResult{}, err
	}

	// 11. Build authoritative response and session facts (Requirements 14.6, 18.1, 18.2)
	respFacts := turnFacts.ToResponseFacts(effectiveModel)
	respFacts.RequestID = requestID
	respFacts.TraceID = traceID
	respFacts.SessionID = string(br.Record.SessionID)
	respFacts.ALegID = aLeg.ALegID
	respFacts.EffectiveModel = effectiveModel
	respFacts.Source = turnFacts.Source.SourceDigest
	respFacts.BodyBytes = turnFacts.Source.BodyBytes
	if respFacts.Operation == "" {
		respFacts.Operation = accepted.WireRequest.Operation
	}
	if respFacts.Delivery == "" {
		respFacts.Delivery = accepted.WireRequest.Delivery
	}

	sessionCarrier := prep.ResponseCarrier(br)

	// Wrap canonical stream with request authority release and terminal leg append upon completion/close
	cleanupStream := &wireLifecycleEventStream{
		EventStream: attemptOut.stream,
		cleanup: func(causeErr error) {
			activeAttempt := attemptOut
			if op, ok := attemptOut.stream.(wireOutcomeProvider); ok {
				activeAttempt = op.currentOutcome()
			}
			if activeAttempt.cancel != nil {
				activeAttempt.cancel()
			}
			if activeAttempt.bodyCloser != nil {
				_ = activeAttempt.bodyCloser.Close()
			}
			if src != nil {
				_ = src.Close()
			}
			_ = e.releaseRequestAuthority(outCtx)

			outcome := billing.LegOutcomeWinner
			if causeErr != nil && !errors.Is(causeErr, io.EOF) {
				if errors.Is(causeErr, context.Canceled) || errors.Is(causeErr, context.DeadlineExceeded) ||
					(outCtx != nil && outCtx.Err() != nil) || (ctx != nil && ctx.Err() != nil) {
					outcome = billing.LegOutcomeCanceled
				} else {
					outcome = billing.LegOutcomeFailed
				}
			}
			if activeAttempt.session == nil || activeAttempt.session.claimBillingLegRecord() {
				e.appendIndependentTerminalLeg(outCtx, billingState, aLeg.ALegID, activeAttempt.bleg, activeAttempt.cand.Primary, activeAttempt.startedAt, e.now(), outcome)
			}

			if aScope != nil {
				aScope.ReleaseBLeg(activeAttempt.bleg.BLegID)
				aScope.End()
			}
			if e.SecureSession != nil {
				turnOutcomeKind := app.TurnOutcomeSuccess
				if outcome != billing.LegOutcomeWinner {
					turnOutcomeKind = app.TurnOutcomeSurfacedFailure
				}
				_ = e.SecureSession.FinishTurn(context.WithoutCancel(outCtx), br.Record.SessionID, br.TurnID, app.TurnOutcome{
					Kind: turnOutcomeKind,
				})
			}
		},
	}

	committed = true
	turnFinished = true
	return largebody.ExecutionResult{
		Stream:  cleanupStream,
		Facts:   respFacts,
		Session: sessionCarrier,
	}, nil
}

// resolveWireTurnFacts constructs bounded WireTurnFacts from accepted facts, source facts,
// and request context (e.g. SessionInput and ClientTurnShape).
func (e *Executor) resolveWireTurnFacts(
	ctx context.Context,
	accepted largebody.Assessment,
	src largebody.Source,
	srcDigest largebody.SourceDigest,
) largebody.WireTurnFacts {
	reqID := accepted.Stamp.GenerationID()
	traceID := reqID
	if wid, ok := largebody.WireIdentityFromContext(ctx); ok {
		if wid.RequestID != "" {
			reqID = wid.RequestID
		}
		if wid.TraceID != "" {
			traceID = wid.TraceID
		} else {
			traceID = reqID
		}
	}
	if reqID == "" {
		reqID = "wire-req"
		traceID = reqID
	}
	candModel := accepted.WireRequest.CandidateModel
	if candModel == "" {
		candModel = accepted.WireRequest.ClientModel
	}

	bodyBytes := accepted.Stamp.BodyBytes()
	if src != nil {
		bodyBytes = src.Size()
	}

	sessInput := largebody.SessionInput{NewSessionRequested: true}
	if si, ok := largebody.WireSessionInputFromContext(ctx); ok {
		sessInput = si
	}

	turnShape := largebody.ClientTurnShape{}
	if ts, ok := largebody.WireClientTurnShapeFromContext(ctx); ok {
		turnShape = ts
	}

	return largebody.WireTurnFacts{
		Route: largebody.WireRouteFacts{
			ProfileID:      accepted.Stamp.ProfileID(),
			ClientModel:    accepted.WireRequest.ClientModel,
			CandidateModel: candModel,
			MaxAttempts:    largebody.DefaultWireMaxAttempts,
		},
		Protocol: largebody.WireProtocolFacts{
			Operation: accepted.WireRequest.Operation,
			Delivery:  accepted.WireRequest.Delivery,
		},
		MaxOutput: largebody.WireMaxOutputFacts{
			MaxOutputTokens: accepted.WireRequest.MaxOutputTokens,
		},
		Identity: largebody.WireIdentityFacts{
			RequestID:       reqID,
			TraceID:         traceID,
			CanonicalDigest: largebody.NewIdentityDigest(srcDigest.Sum()),
			CheckpointID:    "customer-request:" + reqID,
		},
		Session: largebody.WireSessionFacts{
			Input:     sessInput,
			TurnShape: turnShape,
		},
		Source: largebody.WireSourceFacts{
			SourceDigest: srcDigest,
			BodyBytes:    bodyBytes,
			BodyMode:     accepted.Stamp.BodyMode(),
		},
		Rewrite: largebody.WireRewriteFacts{
			Semantics: accepted.Stamp.Rewrite(),
		},
		Economic: largebody.WireEconomicFacts{
			BillingCallID:     reqID,
			RequestCount:      1,
			MaxOutputQuantity: accepted.WireRequest.MaxOutputTokens,
		},
	}
}

// openFreshWireBody opens an independent offset-zero reader over src, applying
// candidate model splicing only if approved by the assessment rewrite (Requirements 9, 10.1, 10.2).
func openFreshWireBody(src largebody.Source, accepted largebody.Assessment, candidateModel string) (io.ReadCloser, int64, error) {
	if src == nil {
		return nil, 0, fmt.Errorf("largebody: nil source")
	}
	rewrite := accepted.Stamp.Rewrite()
	if rewrite.NeedsModelRewrite() {
		span := rewrite.Span()
		splice, err := largebody.SpliceModelToken(src, span, candidateModel)
		if err != nil {
			return nil, 0, fmt.Errorf("largebody: splice model token: %w", err)
		}
		return splice, splice.RewrittenLength(), nil
	}
	rc, err := src.Open()
	if err != nil {
		return nil, 0, fmt.Errorf("largebody: open source: %w", err)
	}
	return rc, src.Size(), nil
}

// wireAttemptPayload carries wire-mode payload facts through canonical candidate evaluation and attempt transactions.
type wireAttemptPayload struct {
	src             largebody.Source
	accepted        largebody.Assessment
	turnFacts       largebody.WireTurnFacts
	requestID       string
	sessionID       string
	maxOutputTokens *int
}

// wireBodyClosingStream ensures the fresh wire body reader is closed whenever the stream terminates.
type wireBodyClosingStream struct {
	lipapi.ManagedEventStream
	closer io.Closer
	cancel context.CancelFunc
	once   sync.Once
}

func (s *wireBodyClosingStream) Close() error {
	var closeErr error
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.closer != nil {
			closeErr = s.closer.Close()
		}
	})
	streamErr := s.ManagedEventStream.Close()
	if closeErr != nil {
		return closeErr
	}
	return streamErr
}

func (s *wireBodyClosingStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.closer != nil {
			_ = s.closer.Close()
		}
	})
	return s.ManagedEventStream.Cancel(ctx, cause)
}

func (e *Executor) openWireAttemptTx(
	ctx context.Context,
	tx *attemptTx,
	be execbackend.Backend,
	beOK bool,
	c routing.AttemptCandidate,
	cPlan candidatePlan,
) error {
	if tx.budget != nil {
		if !tx.budget.tryAcquire() {
			return fmt.Errorf("executor: %w", lipapi.ErrMaxRouteAttempts)
		}
		tx.budgetAcquired = true
	}

	if !beOK {
		err := fmt.Errorf("executor: backend %q not configured", c.Primary.Backend)
		tx.recordFailure(ctx, lipapi.AttemptSwallowedFailure, err.Error(), err)
		tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindSwallowed, billing.LegOutcomeNeverStarted, err, "")
		return nil
	}

	parentCtx := ctx
	if cPlan.parallel && cPlan.parentCtx != nil {
		parentCtx = cPlan.parentCtx
	}
	legCtx, legCancel := context.WithCancel(parentCtx)
	openedOK := false
	defer func() {
		if !openedOK {
			legCancel()
		}
	}()

	var cancelOpen context.CancelFunc = func() {}
	ttftDeadline := ttftContextDeadline{}
	openCtx := legCtx
	if tx.failures != nil && tx.failures.progress != nil && tx.failures.progress.ttft != nil {
		openCtx, cancelOpen, ttftDeadline = tx.failures.progress.ttft.scopedContext(legCtx, e.now(), c.Key, c.Primary.TTFTTimeout)
	}
	defer cancelOpen()

	var stopWatcher func() bool
	if ttftDeadline.scope != ttftTimeoutNone {
		stopWatcher = context.AfterFunc(openCtx, legCancel)
	}

	var stopArmWatcher func() bool
	if cPlan.parallel {
		stopArmWatcher = context.AfterFunc(ctx, legCancel)
	}

	baseOpenCtx := legCtx
	if tx.reqFacts.aScope != nil {
		permitCtx, permit, perr := tx.reqFacts.aScope.BeginBLegLaunch(legCtx, tx.bleg.BLegID)
		if perr != nil {
			if stopWatcher != nil {
				stopWatcher()
			}
			if stopArmWatcher != nil {
				stopArmWatcher()
			}
			legCancel()
			if errors.Is(perr, leglifecycle.ErrALegCanceled) {
				tx.recordFailure(ctx, lipapi.AttemptCancelled, "a-leg canceled before launch", perr)
				tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindAdmissionFailure, billing.LegOutcomeNeverStarted, perr, "a-leg canceled before launch")
				return nil
			}
			return perr
		}
		tx.launchPermit = permit
		baseOpenCtx = permitCtx
	}

	wp := tx.reqFacts.wirePayload
	openStart := e.now()
	tx.openStartedAt = openStart

	holder := tx.reqFacts.metering
	if holder == nil {
		holder = meteringHolderFrom(ctx)
	}
	if holder != nil {
		_, ingErr := e.CaptureWireBackendIngress(baseOpenCtx, holder, WireBackendIngressArgs{
			RequestID:       wp.requestID,
			TraceID:         tx.reqFacts.traceID,
			AttemptID:       tx.bleg.BLegID,
			BLegID:          tx.bleg.BLegID,
			ALegID:          tx.reqFacts.aLegID,
			SessionID:       wp.sessionID,
			Scope:           tx.reqFacts.recvViews.Scope,
			BackendID:       c.Primary.Backend,
			Model:           c.Primary.Model,
			MaxOutputTokens: wp.maxOutputTokens,
			Now:             openStart,
			SourceDigest:    wp.turnFacts.Source.SourceDigest.Sum(),
		})
		if ingErr != nil {
			tx.abortLaunchPermit()
			tx.recordFailure(ctx, lipapi.AttemptSurfacedFailure, ingErr.Error(), ingErr)
			tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindAdmissionFailure, billing.LegOutcomeFailed, ingErr, "")
			return fmt.Errorf("executor: backend ingress checkpoint: %w", ingErr)
		}
	}

	bodyReader, contentLength, openErr := openFreshWireBody(wp.src, wp.accepted, c.Primary.Model)
	if openErr != nil {
		tx.abortLaunchPermit()
		tx.recordFailure(ctx, lipapi.AttemptSwallowedFailure, openErr.Error(), openErr)
		tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindSwallowed, billing.LegOutcomeNeverStarted, openErr, "")
		return nil
	}

	op := wp.turnFacts.Protocol.Operation
	if op == "" {
		op = wp.accepted.WireRequest.Operation
	}
	del := wp.turnFacts.Protocol.Delivery
	if del == "" {
		del = wp.accepted.WireRequest.Delivery
	}

	wireReq := largebody.WireOpenRequest{
		Candidate:     c,
		Body:          bodyReader,
		ContentLength: contentLength,
		WireRequest: largebody.WireRequestFacts{
			ProfileID:       wp.turnFacts.Route.ProfileID,
			Operation:       op,
			Delivery:        del,
			BodyMode:        wp.turnFacts.Source.BodyMode,
			Rewrite:         wp.turnFacts.Rewrite.Semantics,
			ClientModel:     wp.turnFacts.Route.ClientModel,
			CandidateModel:  c.Primary.Model,
			MaxOutputTokens: wp.turnFacts.MaxOutput.MaxOutputTokens,
		},
		TraceID: tx.reqFacts.traceID,
		ALegID:  tx.reqFacts.aLegID,
		BLegID:  tx.bleg.BLegID,
	}

	openSpanCtx, openSpan := otel.Tracer(otelScopeExecutor).Start(
		baseOpenCtx, "lip.executor.backend_wire_open",
		trace.WithAttributes(
			attribute.String("lip.backend", c.Primary.Backend),
			attribute.Int("lip.b_leg_seq", int(tx.bleg.Seq)),
		),
	)
	defer openSpan.End()

	tx.openInvoked = true
	tx.backendAttempted = true

	stream, err := safety.CallValue(safety.BoundaryBackend, "backend_open", func() (lipapi.ManagedEventStream, error) {
		return execbackend.EffectiveWireOpen(openSpanCtx, be, wireReq)
	})
	openDur := time.Since(openStart).Seconds()
	if e.Metrics != nil {
		e.Metrics.OnBackendOpenDuration(c.Primary.Backend, openDur)
	}

	if err != nil {
		if stopWatcher != nil {
			stopWatcher()
		}
		if stopArmWatcher != nil {
			stopArmWatcher()
		}
		legCancel()
		_ = bodyReader.Close()
		tx.abortLaunchPermit()
		var pe *safety.PanicError
		if errors.As(err, &pe) {
			err = mapBackendPanic(pe, false, c.Key)
		}
		isTTFT := (ttftDeadline.scope != ttftTimeoutNone) &&
			(ttftDeadline.expired(openCtx, err) || (openCtx.Err() == context.DeadlineExceeded && (parentCtx == nil || parentCtx.Err() == nil)))
		if isTTFT {
			tf := ttftFailure(ttftDeadline.scope, c.Key)
			if ttftDeadline.scope == ttftTimeoutLeaf {
				tx.recordFailure(ctx, lipapi.AttemptSwallowedFailure, ttftAttemptReason(ttftDeadline.scope), tf)
				tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindSwallowed, billing.LegOutcomeNeverStarted, nil, "")
				return nil
			}
			tx.recordFailure(ctx, lipapi.AttemptSurfacedFailure, ttftAttemptReason(ttftDeadline.scope), tf)
			tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindLosing, billing.LegOutcomeNeverStarted, nil, "")
			return fmt.Errorf("executor: backend open %q: %w", c.Primary.Backend, lipapi.ErrTTFTTimeout)
		}
		openSpan.RecordError(err)
		openSpan.SetStatus(codes.Error, "backend open failed")
		if lipapi.IsRecoverablePreOutput(err) {
			if cPlan.stickyBinding && c.Primary.Backend == cPlan.stickyBackendID {
				if cPlan.parallel {
					if tx.failures != nil {
						tx.failures.AffinityReset = "recoverable_pre_output_open"
					}
				} else {
					e.clearAffinityBinding(ctx, tx.reqFacts.traceID, tx.routeFacts.affinityKey, tx.routeFacts.affinitySet, "recoverable_pre_output_open")
				}
			}
			tx.recordFailure(ctx, lipapi.AttemptSwallowedFailure, "recoverable pre-output (open)", err)
			diag.LogDecision(
				ctx, e.Log, "recoverable_pre_output_swallowed",
				diag.AttrOpts{CallID: tx.reqFacts.traceID, BLegID: tx.bleg.BLegID},
				slog.String("candidate_key", c.Key),
				slog.String("phase", "open"),
			)
			tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindSwallowed, billing.LegOutcomeNeverStarted, err, "recoverable pre-output (open)")
			return nil
		}
		recordOutcome := lipapi.AttemptSurfacedFailure
		recordReason := attemptReasonDetail(err)
		releaseKind := authorityapp.ReleaseKindLosing
		legOutcome := billing.LegOutcomeNeverStarted
		if errors.Is(err, context.Canceled) && legCtx.Err() == nil {
			releaseKind = authorityapp.ReleaseKindAdmissionFailure
		} else if cPlan.parallel && errors.Is(err, context.Canceled) {
			parentCanceled := cPlan.parentCtx != nil && cPlan.parentCtx.Err() != nil
			if !parentCanceled {
				recordOutcome = lipapi.AttemptCancelled
				recordReason = "parallel race loser"
				releaseKind = authorityapp.ReleaseKindAdmissionFailure
				tx.backendAttempted = false
				legOutcome = billing.LegOutcomeCanceled
			}
		} else if cPlan.parallel {
			legOutcome = billing.LegOutcomeFailed
		}
		tx.recordFailure(ctx, recordOutcome, recordReason, err)
		tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, releaseKind, legOutcome, nil, "")
		return fmt.Errorf("executor: backend open %q: %w", c.Primary.Backend, err)
	}

	if stopWatcher != nil {
		stopWatcher()
	}
	if stopArmWatcher != nil {
		stopArmWatcher()
	}

	if m := e.secureSessionForAttempt(); m != nil {
		if tx.reqFacts.secureTurnOK {
			tr := buildAttemptTrace(tx.reqFacts.secureTurn, tx.reqFacts.aLegID, tx.bleg, c, lipapi.Call{}, openStart)
			persistCtx := context.WithoutCancel(openSpanCtx)
			if rerr := m.RecordAttemptOpened(persistCtx, tr); rerr != nil && e.Log != nil {
				e.Log.DebugContext(persistCtx, "secure_session_attempt_trace_failed", "error", rerr)
			}
		}
	}

	tx.stream = &wireBodyClosingStream{
		ManagedEventStream: stream,
		closer:             bodyReader,
		cancel:             legCancel,
	}
	tx.accounting = newAttemptAccountingTracker(openStart)
	tx.recordAttemptLoggedFn = e.recordAttemptLogged

	if c.MarkedFirst && !cPlan.parallel {
		if err := e.Store.SetWeightedFirstConsumed(ctx, tx.reqFacts.aLegID, true); err != nil {
			return fmt.Errorf("executor: set weighted first consumed: %w", err)
		}
	}
	openedOK = true
	return nil
}

// wireAttemptInput bundles arguments for executing wire attempts under existing attempt ownership.
type wireAttemptInput struct {
	ctx                   context.Context
	outCtx                context.Context
	accepted              largebody.Assessment
	src                   largebody.Source
	turnFacts             largebody.WireTurnFacts
	traceID               string
	requestID             string
	aLegID                string
	sessionID             string
	effectiveModel        string
	maxOutputTokens       *int
	aScope                *leglifecycle.ALeg
	billingState          *billingCallState
	weightedFirstConsumed bool
}

// wireAttemptOutcome captures the winning attempt results.
type wireAttemptOutcome struct {
	stream     lipapi.ManagedEventStream
	cand       routing.AttemptCandidate
	bleg       b2bua.BLegRecord
	startedAt  time.Time
	cancel     context.CancelFunc
	bodyCloser io.Closer
	session    *attemptSession
}

func (e *Executor) newWireRecoveryController(
	budget *attemptBudget,
	ttft *ttftBudget,
	sessionState *routing.SessionRoutingState,
	requestSize routing.RequestSizeEstimate,
	affinityKey affinity.Key,
	affinityKeyOK bool,
	excluded map[string]struct{},
	rng routing.Rng,
	sel *routing.Selector,
) *recoveryController {
	var recIn recoveryControllerInput
	recIn.e = e
	recIn.affinityStore = e.AffinityStore
	recIn.log = e.Log
	recIn.budget = budget
	recIn.ttft = ttft
	recIn.sel = sel
	recIn.requestSize = requestSize
	recIn.session = sessionState
	recIn.excluded = excluded
	recIn.rng = rng
	recIn.affinityKey = affinityKey
	recIn.affinitySet = affinityKeyOK
	return newRecoveryController(recIn)
}

func makeWireRequestFacts(wireIn wireAttemptInput, views execctx.Views, pOK bool, st execctx.SecureSessionTurn, stOK bool) requestFacts {
	var rf requestFacts
	rf.traceID = wireIn.traceID
	rf.aLegID = wireIn.aLegID
	rf.billingCallState = wireIn.billingState
	rf.recvViews = views
	rf.recvViewsOK = pOK
	rf.secureTurn = st
	rf.secureTurnOK = stOK
	rf.aScope = wireIn.aScope
	rf.wirePayload = &wireAttemptPayload{
		src:             wireIn.src,
		accepted:        wireIn.accepted,
		turnFacts:       wireIn.turnFacts,
		requestID:       wireIn.requestID,
		sessionID:       wireIn.sessionID,
		maxOutputTokens: wireIn.maxOutputTokens,
	}
	return rf
}

// executeWireAttempts executes attempts using canonical openNext routing machinery.
func (e *Executor) executeWireAttempts(wireIn wireAttemptInput) (wireAttemptOutcome, error) {
	_, sel, err := routing.ComposeInitialCandidates(
		wireIn.effectiveModel,
		e.SelectorAliases,
		e.DefaultBackend,
		e.BackendExecutionResolver,
		e.ExecutionCompositionPolicy,
		nil,
	)
	if err != nil {
		return wireAttemptOutcome{}, fmt.Errorf("executor: route selector: %w", err)
	}

	views := execctx.Views{}
	p, pOK := execview.PrincipalFromContext(wireIn.outCtx)
	if pOK {
		views.Principal = p
	}
	affinityKey, affinityKeyOK, err := e.resolveAffinityKey(sel, views, pOK)
	if err != nil {
		return wireAttemptOutcome{}, fmt.Errorf("executor: affinity identity: %w", err)
	}

	failures := &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}}
	budget := &attemptBudget{
		max:      e.effectiveMaxAttempts(),
		used:     0,
		failures: failures,
	}
	ttft := newTTFTBudget(e.now(), sel)
	sessionState := &routing.SessionRoutingState{FirstRequestConsumed: wireIn.weightedFirstConsumed}
	excluded := map[string]struct{}{}
	rng := e.rng()
	requestSize := routing.RequestSizeEstimate{Available: true, Tokens: wireIn.turnFacts.Source.BodyBytes, Basis: "wire_body_bytes"}

	progress := e.newWireRecoveryController(budget, ttft, sessionState, requestSize, affinityKey, affinityKeyOK, excluded, rng, sel)

	st, stOK := execctx.SecureSessionTurnFromContext(wireIn.outCtx)
	rf := makeWireRequestFacts(wireIn, views, pOK, st, stOK)

	route := routeFacts{
		sel:         sel,
		requestSize: requestSize,
		affinityKey: affinityKey,
		affinitySet: affinityKeyOK,
		rng:         rng,
	}

	for {
		if wireIn.outCtx != nil && wireIn.outCtx.Err() != nil {
			return wireAttemptOutcome{}, wireIn.outCtx.Err()
		}
		if wireIn.aScope != nil && wireIn.aScope.Err() != nil {
			return wireAttemptOutcome{}, leglifecycle.ErrALegCanceled
		}

		openOut, err := e.openNext(wireIn.outCtx, openNextRequest{
			reqFacts:   rf,
			routeFacts: route,
			progress:   progress,
			mode:       openModeInitial,
		})
		if err != nil {
			return wireAttemptOutcome{}, err
		}
		if openOut.ready == nil {
			continue
		}

		cand := openOut.ready.Candidate()
		bleg := openOut.ready.BLeg()
		// boundSess is write-once at construction in attempt_session.go before publication; all other accesses under r.mu; no concurrent writer exists, open->wire handoff provides happens-before.
		session := openOut.ready.boundSess
		stream, startedAt, err := openOut.ready.WireTakeStream()
		if err != nil {
			return wireAttemptOutcome{}, err
		}

		progress.ttft.markCommitted()

		e.recordAttemptLogged(wireIn.outCtx, recordAttemptParams{
			ALegID:  wireIn.aLegID,
			BLeg:    bleg,
			Cand:    cand,
			Outcome: lipapi.AttemptSuccess,
		}, diag.AttrOpts{CallID: wireIn.traceID})

		if affinityKeyOK && affinityKey.Valid() && e.AffinityStore != nil {
			binding := affinity.BindingFromCandidate(affinityKey, cand, e.now(), "output_committed")
			_ = e.AffinityStore.Set(context.WithoutCancel(wireIn.outCtx), binding)
			e.noteRouteDecision(context.WithoutCancel(wireIn.outCtx), wireIn.traceID, "affinity_bind", binding.BackendID)
		}

		initialAttempt := wireAttemptOutcome{
			stream:    stream,
			cand:      cand,
			bleg:      bleg,
			startedAt: startedAt,
			session:   session,
		}
		recStream := e.newWireRecoveryStream(wireIn, rf, route, progress, affinityKey, affinityKeyOK, initialAttempt)

		return wireAttemptOutcome{
			stream:    recStream,
			cand:      cand,
			bleg:      bleg,
			startedAt: startedAt,
			session:   session,
		}, nil
	}
}

type wireParallelRaceResult struct {
	opened     bool
	stream     lipapi.ManagedEventStream
	cand       routing.AttemptCandidate
	bleg       b2bua.BLegRecord
	startedAt  time.Time
	cancel     context.CancelFunc
	bodyCloser io.Closer
	session    *attemptSession
}

// executeWireParallelRace executes a parallel race across candidates by delegating to the canonical tryOpenParallelGroup.
func (e *Executor) executeWireParallelRace(
	wireIn wireAttemptInput,
	candidates []routing.AttemptCandidate,
	budget *attemptBudget,
	failures *candidateFailureHistory,
	excluded map[string]struct{},
	ttft *ttftBudget,
	affinityKey affinity.Key,
	affinityKeyOK bool,
) (wireParallelRaceResult, error) {
	views := execctx.Views{}
	p, pOK := execview.PrincipalFromContext(wireIn.outCtx)
	if pOK {
		views.Principal = p
	}

	if budget != nil && budget.failures == nil && failures != nil {
		budget.failures = failures
	}

	sessionState := &routing.SessionRoutingState{FirstRequestConsumed: wireIn.weightedFirstConsumed}
	rng := e.rng()
	requestSize := routing.RequestSizeEstimate{Available: true, Tokens: wireIn.turnFacts.Source.BodyBytes, Basis: "wire_body_bytes"}

	progress := e.newWireRecoveryController(budget, ttft, sessionState, requestSize, affinityKey, affinityKeyOK, excluded, rng, nil)

	st, stOK := execctx.SecureSessionTurnFromContext(wireIn.outCtx)
	rf := makeWireRequestFacts(wireIn, views, pOK, st, stOK)

	route := routeFacts{
		requestSize: requestSize,
		affinityKey: affinityKey,
		affinitySet: affinityKeyOK,
		rng:         rng,
	}

	req := openNextRequest{
		reqFacts:   rf,
		routeFacts: route,
		progress:   progress,
		mode:       openModeInitial,
	}

	openOut, err := e.tryOpenParallelGroup(wireIn.outCtx, req, candidates, nil, "", false)
	if err != nil {
		return wireParallelRaceResult{}, err
	}
	if openOut.ready == nil {
		return wireParallelRaceResult{opened: false}, nil
	}

	cand := openOut.ready.Candidate()
	bleg := openOut.ready.BLeg()
	// boundSess is write-once at construction in attempt_session.go before publication; all other accesses under r.mu; no concurrent writer exists, open->wire handoff provides happens-before.
	session := openOut.ready.boundSess
	stream, startedAt, err := openOut.ready.WireTakeStream()
	if err != nil {
		return wireParallelRaceResult{}, err
	}

	if ttft != nil {
		ttft.markCommitted()
	}

	e.recordAttemptLogged(wireIn.outCtx, recordAttemptParams{
		ALegID:  wireIn.aLegID,
		BLeg:    bleg,
		Cand:    cand,
		Outcome: lipapi.AttemptSuccess,
	}, diag.AttrOpts{CallID: wireIn.traceID})

	if affinityKeyOK && affinityKey.Valid() && e.AffinityStore != nil {
		binding := affinity.BindingFromCandidate(affinityKey, cand, e.now(), "output_committed")
		_ = e.AffinityStore.Set(context.WithoutCancel(wireIn.outCtx), binding)
		e.noteRouteDecision(context.WithoutCancel(wireIn.outCtx), wireIn.traceID, "affinity_bind", binding.BackendID)
	}

	initialAttempt := wireAttemptOutcome{
		stream:    stream,
		cand:      cand,
		bleg:      bleg,
		startedAt: startedAt,
		session:   session,
	}
	recStream := e.newWireRecoveryStream(wireIn, rf, route, progress, affinityKey, affinityKeyOK, initialAttempt)

	return wireParallelRaceResult{
		opened:    true,
		stream:    recStream,
		cand:      cand,
		bleg:      bleg,
		startedAt: startedAt,
		session:   session,
	}, nil
}

type wireOutcomeProvider interface {
	currentOutcome() wireAttemptOutcome
}

type wireRecoveryStream struct {
	e             *Executor
	wireIn        wireAttemptInput
	rf            requestFacts
	route         routeFacts
	progress      *recoveryController
	affinityKey   affinity.Key
	affinityKeyOK bool

	mu            sync.Mutex
	activeAttempt wireAttemptOutcome
	committed     bool
	closed        bool
}

var _ lipapi.ManagedEventStream = (*wireRecoveryStream)(nil)
var _ wireOutcomeProvider = (*wireRecoveryStream)(nil)

func (e *Executor) newWireRecoveryStream(
	wireIn wireAttemptInput,
	rf requestFacts,
	route routeFacts,
	progress *recoveryController,
	affinityKey affinity.Key,
	affinityKeyOK bool,
	initial wireAttemptOutcome,
) *wireRecoveryStream {
	return &wireRecoveryStream{
		e:             e,
		wireIn:        wireIn,
		rf:            rf,
		route:         route,
		progress:      progress,
		affinityKey:   affinityKey,
		affinityKeyOK: affinityKeyOK,
		activeAttempt: initial,
	}
}

func (s *wireRecoveryStream) currentOutcome() wireAttemptOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeAttempt
}

func (s *wireRecoveryStream) Close() error {
	s.mu.Lock()
	s.closed = true
	active := s.activeAttempt
	s.mu.Unlock()

	if active.stream != nil {
		return active.stream.Close()
	}
	return nil
}

func (s *wireRecoveryStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	s.mu.Lock()
	active := s.activeAttempt
	s.mu.Unlock()

	if active.stream != nil {
		return active.stream.Cancel(ctx, cause)
	}
	return lipapi.CancelResult{Mode: lipapi.CancelModeNone}
}

func (s *wireRecoveryStream) terminalizeSwallowed(ctx context.Context, attempt wireAttemptOutcome, reason string, err error) {
	if attempt.session != nil {
		attempt.session.terminalizeWireSwallowed(ctx, s.wireIn.traceID, s.wireIn.aLegID, attempt.startedAt, reason, err)
	}
	if attempt.cancel != nil {
		attempt.cancel()
	}
	if attempt.bodyCloser != nil {
		_ = attempt.bodyCloser.Close()
	}
}

func (s *wireRecoveryStream) openNextCandidate(ctx context.Context) (wireAttemptOutcome, error) {
	for {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return wireAttemptOutcome{}, io.EOF
		}

		if ctx != nil && ctx.Err() != nil {
			return wireAttemptOutcome{}, ctx.Err()
		}
		if s.wireIn.outCtx != nil && s.wireIn.outCtx.Err() != nil {
			return wireAttemptOutcome{}, s.wireIn.outCtx.Err()
		}
		if s.wireIn.aScope != nil && s.wireIn.aScope.Err() != nil {
			return wireAttemptOutcome{}, leglifecycle.ErrALegCanceled
		}

		openOut, err := s.e.openNext(s.wireIn.outCtx, openNextRequest{
			reqFacts:   s.rf,
			routeFacts: s.route,
			progress:   s.progress,
			mode:       openModeRetry,
		})
		if err != nil {
			return wireAttemptOutcome{}, err
		}
		if openOut.ready == nil {
			continue
		}

		cand := openOut.ready.Candidate()
		bleg := openOut.ready.BLeg()
		// boundSess is write-once at construction in attempt_session.go before publication; all other accesses under r.mu; no concurrent writer exists, open->wire handoff provides happens-before.
		session := openOut.ready.boundSess
		stream, startedAt, err := openOut.ready.WireTakeStream()
		if err != nil {
			return wireAttemptOutcome{}, err
		}

		s.mu.Lock()
		closed = s.closed
		s.mu.Unlock()
		if closed {
			if session != nil {
				session.terminalizeWireSwallowed(ctx, s.wireIn.traceID, s.wireIn.aLegID, startedAt, "stream closed during recovery", io.EOF)
			}
			return wireAttemptOutcome{}, io.EOF
		}

		if s.progress != nil && s.progress.ttft != nil {
			s.progress.ttft.markCommitted()
		}

		if s.e != nil {
			s.e.recordAttemptLogged(s.wireIn.outCtx, recordAttemptParams{
				ALegID:  s.wireIn.aLegID,
				BLeg:    bleg,
				Cand:    cand,
				Outcome: lipapi.AttemptSuccess,
			}, diag.AttrOpts{CallID: s.wireIn.traceID})
		}

		if s.affinityKeyOK && s.affinityKey.Valid() && s.e != nil && s.e.AffinityStore != nil {
			binding := affinity.BindingFromCandidate(s.affinityKey, cand, s.e.now(), "output_committed")
			_ = s.e.AffinityStore.Set(context.WithoutCancel(s.wireIn.outCtx), binding)
			s.e.noteRouteDecision(context.WithoutCancel(s.wireIn.outCtx), s.wireIn.traceID, "affinity_bind", binding.BackendID)
		}

		return wireAttemptOutcome{
			stream:    stream,
			cand:      cand,
			bleg:      bleg,
			startedAt: startedAt,
			session:   session,
		}, nil
	}
}

func (s *wireRecoveryStream) Recv(ctx context.Context) (lipapi.Event, error) {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return lipapi.Event{}, io.EOF
		}
		active := s.activeAttempt
		s.mu.Unlock()

		if active.stream == nil {
			return lipapi.Event{}, io.EOF
		}

		ev, err := active.stream.Recv(ctx)
		if err == nil {
			s.mu.Lock()
			if !s.committed && (lipapi.OutputCommitted(ev) || ev.Kind == lipapi.EventResponseFinished) {
				s.committed = true
			}
			s.mu.Unlock()
			return ev, nil
		}

		if errors.Is(err, io.EOF) {
			return lipapi.Event{}, io.EOF
		}

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || (ctx != nil && ctx.Err() != nil) {
			return lipapi.Event{}, err
		}

		s.mu.Lock()
		committed := s.committed
		s.mu.Unlock()

		if !committed && lipapi.IsRecoverablePreOutput(err) {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				s.terminalizeSwallowed(ctx, active, "stream closed during recovery", err)
				return lipapi.Event{}, io.EOF
			}

			s.terminalizeSwallowed(ctx, active, "recoverable pre-output (recv)", err)
			s.progress.exclude(active.cand.Key)

			nextAttempt, nextErr := s.openNextCandidate(ctx)
			if nextErr != nil {
				return lipapi.Event{}, nextErr
			}

			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				s.terminalizeSwallowed(ctx, nextAttempt, "stream closed during recovery", io.EOF)
				return lipapi.Event{}, io.EOF
			}
			s.activeAttempt = nextAttempt
			s.mu.Unlock()
			continue
		}

		if committed && lipapi.IsRecoverablePreOutput(err) {
			return lipapi.Event{}, &lipapi.UpstreamFailureError{
				Phase:        lipapi.PhasePostOutput,
				Recoverable:  false,
				Reason:       attemptReasonDetail(err),
				CandidateKey: active.cand.Key,
			}
		}

		return lipapi.Event{}, err
	}
}
