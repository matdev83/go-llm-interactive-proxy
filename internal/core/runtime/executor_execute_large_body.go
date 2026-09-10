package runtime

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

var _ largebody.LargeBodyWireExecutor = (*Executor)(nil)

// wireLifecycleEventStream wraps an EventStream to release request authority
// once when the stream is closed or consumed to EOF.
type wireLifecycleEventStream struct {
	lipapi.EventStream
	cleanup func()
	once    sync.Once
}

func (s *wireLifecycleEventStream) Close() error {
	s.once.Do(s.cleanup)
	if s.EventStream != nil {
		return s.EventStream.Close()
	}
	return nil
}

func (s *wireLifecycleEventStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.EventStream == nil {
		s.once.Do(s.cleanup)
		return lipapi.Event{}, io.EOF
	}
	ev, err := s.EventStream.Recv(ctx)
	if err != nil {
		s.once.Do(s.cleanup)
	}
	return ev, err
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

	primary := routing.Primary{Model: effectiveModel}
	sel := &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}}

	if _, err := e.AuthorizeWireBilling(outCtx, WireBillingExposureArgs{
		BillingCallID:   billingCallID,
		TraceID:         traceID,
		ALegID:          aLeg.ALegID,
		SessionID:       string(br.Record.SessionID),
		Scope:           prep.Scope(),
		Route:           sel,
		RequestSize:     routing.RequestSizeEstimate{},
		MaxOutputTokens: maxOutputTokens,
	}); err != nil {
		return largebody.ExecutionResult{}, err
	}

	// 10. Build authoritative response and session facts (Requirements 14.6, 18.1, 18.2)
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

	// Wrap canonical stream with request authority release upon completion/close
	rawStream := lipapi.NewFixedEventStream(nil)
	cleanupStream := &wireLifecycleEventStream{
		EventStream: rawStream,
		cleanup: func() {
			_ = e.releaseRequestAuthority(outCtx)
		},
	}

	committed = true
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
