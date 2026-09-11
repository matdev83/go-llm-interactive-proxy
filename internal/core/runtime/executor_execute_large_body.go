package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

var _ largebody.LargeBodyWireExecutor = (*Executor)(nil)

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

// wireReadyAttempt provides the ready lifecycle handle adapter for wire launch permit commit.
type wireReadyAttempt struct {
	stream lipapi.ManagedEventStream
}

func (r wireReadyAttempt) lifecycleHandle() leglifecycle.BLegAttempt {
	return r.stream
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
			if attemptOut.cancel != nil {
				attemptOut.cancel()
			}
			if attemptOut.bodyCloser != nil {
				_ = attemptOut.bodyCloser.Close()
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
			e.appendIndependentTerminalLeg(outCtx, billingState, aLeg.ALegID, attemptOut.bleg, attemptOut.cand.Primary, attemptOut.startedAt, e.now(), outcome)

			if aScope != nil {
				aScope.ReleaseBLeg(attemptOut.bleg.BLegID)
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

// wirePrependFirstStream yields one buffered event already peeked from the upstream,
// then delegates to rest for subsequent events (Requirements 10.4, 12.5).
type wirePrependFirstStream struct {
	first    lipapi.Event
	hasFirst bool
	rest     lipapi.ManagedEventStream
}

func (s *wirePrependFirstStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.hasFirst {
		s.hasFirst = false
		return s.first, nil
	}
	if s.rest == nil {
		return lipapi.Event{}, io.EOF
	}
	return s.rest.Recv(ctx)
}

func (s *wirePrependFirstStream) Close() error {
	if s.rest == nil {
		return nil
	}
	return s.rest.Close()
}

func (s *wirePrependFirstStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	if s.rest == nil {
		return lipapi.CancelResult{}
	}
	return s.rest.Cancel(ctx, cause)
}

// peekFirstWireEvent peeks the first event from a managed stream and returns a prepended
// stream. If the first Recv fails, the stream is closed and the error returned (Requirement 10.4).
func peekFirstWireEvent(ctx context.Context, es lipapi.ManagedEventStream) (lipapi.ManagedEventStream, lipapi.Event, error) {
	if es == nil {
		return nil, lipapi.Event{}, io.EOF
	}
	ev, err := es.Recv(ctx)
	if err != nil {
		_ = es.Close()
		return nil, lipapi.Event{}, err
	}
	return &wirePrependFirstStream{first: ev, hasFirst: true, rest: es}, ev, nil
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
}

// executeWireAttempts runs the attempt-open and failover loop across expanded groups (Requirements 8, 9, 10, 12, 15).
func (e *Executor) executeWireAttempts(in wireAttemptInput) (wireAttemptOutcome, error) {
	_, sel, err := routing.ComposeInitialCandidates(
		in.effectiveModel,
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
	p, pOK := execview.PrincipalFromContext(in.outCtx)
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
	sessionState := &routing.SessionRoutingState{FirstRequestConsumed: in.weightedFirstConsumed}
	excluded := map[string]struct{}{}
	rng := e.rng()

	stickyBackendID, stickyBinding, err := e.lookupAffinityBinding(in.outCtx, in.traceID, sel, affinityKey, affinityKeyOK)
	if err != nil {
		return wireAttemptOutcome{}, err
	}

	for {
		if in.outCtx != nil && in.outCtx.Err() != nil {
			return wireAttemptOutcome{}, in.outCtx.Err()
		}
		if in.aScope != nil && in.aScope.Err() != nil {
			return wireAttemptOutcome{}, leglifecycle.ErrALegCanceled
		}

		opts := routing.PlanOptions{
			Excluded:        excluded,
			Unhealthy:       e.mergePlannerHealth(),
			RequestSize:     routing.RequestSizeEstimate{Available: true, Tokens: in.turnFacts.Source.BodyBytes, Basis: "wire_body_bytes"},
			Session:         sessionState,
			StickyBackendID: stickyBackendID,
			Rand:            rng,
			IsRetryPath:     budget.usedNow() > 0,
		}

		groups, err := routing.ExpandFailoverGroups(sel, opts)
		if stickyBinding && stickyBackendID != "" &&
			(err != nil || len(groups) == 0 || len(groups[0].Candidates) == 0 || groups[0].Candidates[0].Primary.Backend != stickyBackendID) {
			e.clearAffinityBinding(in.outCtx, in.traceID, affinityKey, affinityKeyOK, "ineligible")
			stickyBackendID = ""
			stickyBinding = false
			opts.StickyBackendID = ""
			groups, err = routing.ExpandFailoverGroups(sel, opts)
		}

		if err != nil {
			if errors.Is(err, routing.ErrNoEligibleCandidate) {
				if finalErr := failures.FinalError(err); finalErr != err {
					return wireAttemptOutcome{}, finalErr
				}
			}
			return wireAttemptOutcome{}, fmt.Errorf("executor: expand failover: %w", err)
		}

		var openedThisPass bool
		var outcome wireAttemptOutcome

		for _, group := range groups {
			candidates := group.Candidates
			if len(candidates) == 0 {
				continue
			}

			if candidates[0].IsParallel {
				res, err := e.executeWireParallelRace(in, candidates, budget, failures, excluded, ttft, affinityKey, affinityKeyOK)
				if err != nil {
					return wireAttemptOutcome{}, err
				}
				if res.opened {
					outcome = wireAttemptOutcome{
						stream:     res.stream,
						cand:       res.cand,
						bleg:       res.bleg,
						startedAt:  res.startedAt,
						cancel:     res.cancel,
						bodyCloser: res.bodyCloser,
					}
					openedThisPass = true
					break
				}
				continue
			}

			c := candidates[0]
			if failures != nil {
				failures.ParallelFailure = nil
			}
			if !budget.tryAcquire() {
				return wireAttemptOutcome{}, fmt.Errorf("executor: %w", lipapi.ErrMaxRouteAttempts)
			}

			bleg, err := e.Store.NextBLeg(in.outCtx, in.aLegID)
			if err != nil {
				budget.release()
				return wireAttemptOutcome{}, fmt.Errorf("executor: next b-leg: %w", err)
			}
			if in.billingState != nil {
				in.billingState.noteAllocatedBLeg(bleg.BLegID, bleg.Seq)
			}

			launchCtx, permit, err := in.aScope.BeginBLegLaunch(in.outCtx, bleg.BLegID)
			if err != nil {
				budget.release()
				e.recordAttemptLogged(in.outCtx, recordAttemptParams{
					ALegID:    in.aLegID,
					BLeg:      bleg,
					Cand:      c,
					Outcome:   lipapi.AttemptSwallowedFailure,
					Reason:    err.Error(),
					DetailErr: err,
				}, diag.AttrOpts{CallID: in.traceID})
				e.appendIndependentTerminalLeg(in.outCtx, in.billingState, in.aLegID, bleg, c.Primary, e.now(), e.now(), billing.LegOutcomeNeverStarted)
				excluded[c.Key] = struct{}{}
				continue
			}

			if m := e.secureSessionForAttempt(); m != nil {
				if st, ok := execctx.SecureSessionTurnFromContext(in.outCtx); ok {
					tr := buildAttemptTrace(st, in.aLegID, bleg, c, lipapi.Call{}, e.now())
					persistCtx := context.WithoutCancel(in.outCtx)
					if rerr := m.RecordAttemptOpened(persistCtx, tr); rerr != nil && e.Log != nil {
						e.Log.DebugContext(persistCtx, "secure_session_attempt_trace_failed", "error", rerr)
					}
				}
			}

			legCtx, legCancel := context.WithCancel(launchCtx)
			openCtx, cancelOpen, ttftDeadline := ttft.scopedContext(legCtx, e.now(), c.Key, c.Primary.TTFTTimeout)
			var stopWatcher func() bool
			if ttftDeadline.scope != ttftTimeoutNone {
				stopWatcher = context.AfterFunc(openCtx, legCancel)
			}

			attemptStarted := e.now()

			if holder := meteringHolderFrom(in.outCtx); holder != nil {
				_, ingErr := e.CaptureWireBackendIngress(openCtx, holder, WireBackendIngressArgs{
					RequestID:       in.requestID,
					TraceID:         in.traceID,
					AttemptID:       bleg.BLegID,
					BLegID:          bleg.BLegID,
					ALegID:          in.aLegID,
					SessionID:       in.sessionID,
					Scope:           scope.PrincipalScopeView{},
					BackendID:       c.Primary.Backend,
					Model:           c.Primary.Model,
					MaxOutputTokens: in.maxOutputTokens,
					Now:             attemptStarted,
					SourceDigest:    in.turnFacts.Source.SourceDigest.Sum(),
				})
				if ingErr != nil {
					if stopWatcher != nil {
						stopWatcher()
					}
					cancelOpen()
					legCancel()
					permit.Abort()
					e.recordAttemptLogged(in.outCtx, recordAttemptParams{
						ALegID:    in.aLegID,
						BLeg:      bleg,
						Cand:      c,
						Outcome:   lipapi.AttemptSurfacedFailure,
						Reason:    ingErr.Error(),
						DetailErr: ingErr,
					}, diag.AttrOpts{CallID: in.traceID})
					e.appendIndependentTerminalLeg(in.outCtx, in.billingState, in.aLegID, bleg, c.Primary, attemptStarted, e.now(), billing.LegOutcomeFailed)
					return wireAttemptOutcome{}, fmt.Errorf("executor: backend ingress checkpoint: %w", ingErr)
				}
			}

			bodyReader, contentLength, openErr := openFreshWireBody(in.src, in.accepted, c.Primary.Model)
			if openErr != nil {
				if stopWatcher != nil {
					stopWatcher()
				}
				cancelOpen()
				legCancel()
				permit.Abort()
				budget.release()
				e.recordAttemptLogged(in.outCtx, recordAttemptParams{
					ALegID:    in.aLegID,
					BLeg:      bleg,
					Cand:      c,
					Outcome:   lipapi.AttemptSwallowedFailure,
					Reason:    openErr.Error(),
					DetailErr: openErr,
				}, diag.AttrOpts{CallID: in.traceID})
				e.appendIndependentTerminalLeg(in.outCtx, in.billingState, in.aLegID, bleg, c.Primary, attemptStarted, e.now(), billing.LegOutcomeNeverStarted)
				excluded[c.Key] = struct{}{}
				continue
			}

			wireReq := largebody.WireOpenRequest{
				Candidate:     c,
				Body:          bodyReader,
				ContentLength: contentLength,
				WireRequest: largebody.WireRequestFacts{
					ProfileID:       in.turnFacts.Route.ProfileID,
					Operation:       in.turnFacts.Protocol.Operation,
					Delivery:        in.turnFacts.Protocol.Delivery,
					BodyMode:        in.turnFacts.Source.BodyMode,
					Rewrite:         in.turnFacts.Rewrite.Semantics,
					ClientModel:     in.turnFacts.Route.ClientModel,
					CandidateModel:  c.Primary.Model,
					MaxOutputTokens: in.turnFacts.MaxOutput.MaxOutputTokens,
				},
				TraceID: in.traceID,
				ALegID:  in.aLegID,
				BLegID:  bleg.BLegID,
			}

			be, beOK := e.Backends[c.Primary.Backend]
			if !beOK {
				if stopWatcher != nil {
					stopWatcher()
				}
				cancelOpen()
				legCancel()
				_ = bodyReader.Close()
				permit.Abort()
				err := fmt.Errorf("executor: backend %q not configured", c.Primary.Backend)
				e.recordAttemptLogged(in.outCtx, recordAttemptParams{
					ALegID:    in.aLegID,
					BLeg:      bleg,
					Cand:      c,
					Outcome:   lipapi.AttemptSwallowedFailure,
					Reason:    err.Error(),
					DetailErr: err,
				}, diag.AttrOpts{CallID: in.traceID})
				e.appendIndependentTerminalLeg(in.outCtx, in.billingState, in.aLegID, bleg, c.Primary, attemptStarted, e.now(), billing.LegOutcomeFailed)
				excluded[c.Key] = struct{}{}
				continue
			}

			stream, openErr := execbackend.EffectiveWireOpen(legCtx, be, wireReq)
			if openErr != nil {
				if stopWatcher != nil {
					stopWatcher()
				}
				cancelOpen()
				legCancel()
				_ = bodyReader.Close()
				permit.Abort()

				isTTFT := (ttftDeadline.scope != ttftTimeoutNone) && (ttftDeadline.expired(openCtx, openErr) || openCtx.Err() == context.DeadlineExceeded)
				if isTTFT {
					tf := ttftFailure(ttftDeadline.scope, c.Key)
					if ttftDeadline.scope == ttftTimeoutGlobal {
						e.recordAttemptLogged(in.outCtx, recordAttemptParams{
							ALegID:    in.aLegID,
							BLeg:      bleg,
							Cand:      c,
							Outcome:   lipapi.AttemptSurfacedFailure,
							Reason:    ttftAttemptReason(ttftDeadline.scope),
							DetailErr: tf,
						}, diag.AttrOpts{CallID: in.traceID})
						e.appendIndependentTerminalLeg(in.outCtx, in.billingState, in.aLegID, bleg, c.Primary, attemptStarted, e.now(), billing.LegOutcomeNeverStarted)
						return wireAttemptOutcome{}, fmt.Errorf("executor: backend open %q: %w", c.Primary.Backend, lipapi.ErrTTFTTimeout)
					}
					// Leaf timeout
					e.recordAttemptLogged(in.outCtx, recordAttemptParams{
						ALegID:    in.aLegID,
						BLeg:      bleg,
						Cand:      c,
						Outcome:   lipapi.AttemptSwallowedFailure,
						Reason:    ttftAttemptReason(ttftDeadline.scope),
						DetailErr: tf,
					}, diag.AttrOpts{CallID: in.traceID})
					e.appendIndependentTerminalLeg(in.outCtx, in.billingState, in.aLegID, bleg, c.Primary, attemptStarted, e.now(), billing.LegOutcomeFailed)
					excluded[c.Key] = struct{}{}
					continue
				}

				if !lipapi.IsRecoverablePreOutput(openErr) {
					e.recordAttemptLogged(in.outCtx, recordAttemptParams{
						ALegID:    in.aLegID,
						BLeg:      bleg,
						Cand:      c,
						Outcome:   lipapi.AttemptSurfacedFailure,
						Reason:    attemptReasonDetail(openErr),
						DetailErr: openErr,
					}, diag.AttrOpts{CallID: in.traceID})
					e.appendIndependentTerminalLeg(in.outCtx, in.billingState, in.aLegID, bleg, c.Primary, attemptStarted, e.now(), billing.LegOutcomeFailed)
					return wireAttemptOutcome{}, fmt.Errorf("executor: backend open %q: %w", c.Primary.Backend, openErr)
				}

				if stickyBinding {
					e.clearAffinityBinding(in.outCtx, in.traceID, affinityKey, affinityKeyOK, "recoverable_pre_output_open")
					stickyBackendID = ""
					stickyBinding = false
				}
				e.recordAttemptLogged(in.outCtx, recordAttemptParams{
					ALegID:    in.aLegID,
					BLeg:      bleg,
					Cand:      c,
					Outcome:   lipapi.AttemptSwallowedFailure,
					Reason:    attemptReasonDetail(openErr),
					DetailErr: openErr,
				}, diag.AttrOpts{CallID: in.traceID})
				e.appendIndependentTerminalLeg(in.outCtx, in.billingState, in.aLegID, bleg, c.Primary, attemptStarted, e.now(), billing.LegOutcomeFailed)
				excluded[c.Key] = struct{}{}
				continue
			}

			peekedStream, _, peekErr := peekFirstWireEvent(openCtx, stream)
			if peekErr != nil {
				if stopWatcher != nil {
					stopWatcher()
				}
				cancelOpen()
				legCancel()
				_ = bodyReader.Close()
				permit.Abort()

				isTTFT := (ttftDeadline.scope != ttftTimeoutNone) && (ttftDeadline.expired(openCtx, peekErr) || openCtx.Err() == context.DeadlineExceeded)
				if isTTFT {
					tf := ttftFailure(ttftDeadline.scope, c.Key)
					if ttftDeadline.scope == ttftTimeoutGlobal {
						e.recordAttemptLogged(in.outCtx, recordAttemptParams{
							ALegID:    in.aLegID,
							BLeg:      bleg,
							Cand:      c,
							Outcome:   lipapi.AttemptSurfacedFailure,
							Reason:    ttftAttemptReason(ttftDeadline.scope),
							DetailErr: tf,
						}, diag.AttrOpts{CallID: in.traceID})
						e.appendIndependentTerminalLeg(in.outCtx, in.billingState, in.aLegID, bleg, c.Primary, attemptStarted, e.now(), billing.LegOutcomeNeverStarted)
						return wireAttemptOutcome{}, fmt.Errorf("executor: backend peek %q: %w", c.Primary.Backend, lipapi.ErrTTFTTimeout)
					}
					e.recordAttemptLogged(in.outCtx, recordAttemptParams{
						ALegID:    in.aLegID,
						BLeg:      bleg,
						Cand:      c,
						Outcome:   lipapi.AttemptSwallowedFailure,
						Reason:    ttftAttemptReason(ttftDeadline.scope),
						DetailErr: tf,
					}, diag.AttrOpts{CallID: in.traceID})
					e.appendIndependentTerminalLeg(in.outCtx, in.billingState, in.aLegID, bleg, c.Primary, attemptStarted, e.now(), billing.LegOutcomeFailed)
					excluded[c.Key] = struct{}{}
					continue
				}

				if !lipapi.IsRecoverablePreOutput(peekErr) {
					e.recordAttemptLogged(in.outCtx, recordAttemptParams{
						ALegID:    in.aLegID,
						BLeg:      bleg,
						Cand:      c,
						Outcome:   lipapi.AttemptSurfacedFailure,
						Reason:    attemptReasonDetail(peekErr),
						DetailErr: peekErr,
					}, diag.AttrOpts{CallID: in.traceID})
					e.appendIndependentTerminalLeg(in.outCtx, in.billingState, in.aLegID, bleg, c.Primary, attemptStarted, e.now(), billing.LegOutcomeFailed)
					return wireAttemptOutcome{}, fmt.Errorf("executor: backend peek %q: %w", c.Primary.Backend, peekErr)
				}

				if stickyBinding {
					e.clearAffinityBinding(in.outCtx, in.traceID, affinityKey, affinityKeyOK, "recoverable_pre_output_open")
					stickyBackendID = ""
					stickyBinding = false
				}
				e.recordAttemptLogged(in.outCtx, recordAttemptParams{
					ALegID:    in.aLegID,
					BLeg:      bleg,
					Cand:      c,
					Outcome:   lipapi.AttemptSwallowedFailure,
					Reason:    attemptReasonDetail(peekErr),
					DetailErr: peekErr,
				}, diag.AttrOpts{CallID: in.traceID})
				e.appendIndependentTerminalLeg(in.outCtx, in.billingState, in.aLegID, bleg, c.Primary, attemptStarted, e.now(), billing.LegOutcomeFailed)
				excluded[c.Key] = struct{}{}
				continue
			}

			if stopWatcher != nil {
				stopWatcher()
			}
			cancelOpen()

			ready := wireReadyAttempt{stream: peekedStream}
			commitRes, commitErr := permit.Commit(ready.lifecycleHandle())
			if commitRes.Canceled || errors.Is(commitErr, leglifecycle.ErrALegCanceled) {
				_ = peekedStream.Close()
				legCancel()
				permit.Abort()
				return wireAttemptOutcome{}, leglifecycle.ErrALegCanceled
			}
			if commitErr != nil {
				_ = peekedStream.Close()
				legCancel()
				return wireAttemptOutcome{}, commitErr
			}

			ttft.markCommitted()

			e.recordAttemptLogged(in.outCtx, recordAttemptParams{
				ALegID:  in.aLegID,
				BLeg:    bleg,
				Cand:    c,
				Outcome: lipapi.AttemptSuccess,
			}, diag.AttrOpts{CallID: in.traceID})

			if affinityKeyOK && affinityKey.Valid() && e.AffinityStore != nil {
				binding := affinity.BindingFromCandidate(affinityKey, c, e.now(), "output_committed")
				_ = e.AffinityStore.Set(context.WithoutCancel(in.outCtx), binding)
				e.noteRouteDecision(context.WithoutCancel(in.outCtx), in.traceID, "affinity_bind", binding.BackendID)
			}

			if c.MarkedFirst && !c.IsParallel {
				_ = e.Store.SetWeightedFirstConsumed(in.outCtx, in.aLegID, true)
			}

			outcome = wireAttemptOutcome{
				stream:     peekedStream,
				cand:       c,
				bleg:       bleg,
				startedAt:  attemptStarted,
				cancel:     legCancel,
				bodyCloser: bodyReader,
			}
			openedThisPass = true
			break
		}

		if openedThisPass {
			return outcome, nil
		}
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
}

type wireParallelLeg struct {
	cand       routing.AttemptCandidate
	bleg       b2bua.BLegRecord
	permit     *leglifecycle.LaunchPermit
	launchCtx  context.Context
	cancel     context.CancelFunc
	bodyReader io.ReadCloser
	contentLen int64
	startedAt  time.Time
}

// executeWireParallelRace executes a parallel race across candidates with independent offset-zero readers (Requirement 10.3).
func (e *Executor) executeWireParallelRace(
	in wireAttemptInput,
	candidates []routing.AttemptCandidate,
	budget *attemptBudget,
	failures *candidateFailureHistory,
	excluded map[string]struct{},
	ttft *ttftBudget,
	affinityKey affinity.Key,
	affinityKeyOK bool,
) (wireParallelRaceResult, error) {
	var launched []wireParallelLeg

	for _, cand := range candidates {
		if _, isExcluded := excluded[cand.Key]; isExcluded {
			continue
		}
		if !budget.tryAcquire() {
			break
		}

		bleg, err := e.Store.NextBLeg(in.outCtx, in.aLegID)
		if err != nil {
			budget.release()
			break
		}
		if in.billingState != nil {
			in.billingState.noteAllocatedBLeg(bleg.BLegID, bleg.Seq)
		}

		launchCtx, permit, err := in.aScope.BeginBLegLaunch(in.outCtx, bleg.BLegID)
		if err != nil {
			budget.release()
			e.recordAttemptLogged(in.outCtx, recordAttemptParams{
				ALegID:    in.aLegID,
				BLeg:      bleg,
				Cand:      cand,
				Outcome:   lipapi.AttemptSwallowedFailure,
				Reason:    err.Error(),
				DetailErr: err,
			}, diag.AttrOpts{CallID: in.traceID})
			e.appendIndependentTerminalLeg(in.outCtx, in.billingState, in.aLegID, bleg, cand.Primary, e.now(), e.now(), billing.LegOutcomeNeverStarted)
			excluded[cand.Key] = struct{}{}
			continue
		}

		if m := e.secureSessionForAttempt(); m != nil {
			if st, ok := execctx.SecureSessionTurnFromContext(in.outCtx); ok {
				tr := buildAttemptTrace(st, in.aLegID, bleg, cand, lipapi.Call{}, e.now())
				persistCtx := context.WithoutCancel(in.outCtx)
				if rerr := m.RecordAttemptOpened(persistCtx, tr); rerr != nil && e.Log != nil {
					e.Log.DebugContext(persistCtx, "secure_session_attempt_trace_failed", "error", rerr)
				}
			}
		}

		bodyReader, contentLen, openErr := openFreshWireBody(in.src, in.accepted, cand.Primary.Model)
		if openErr != nil {
			permit.Abort()
			budget.release()
			e.recordAttemptLogged(in.outCtx, recordAttemptParams{
				ALegID:    in.aLegID,
				BLeg:      bleg,
				Cand:      cand,
				Outcome:   lipapi.AttemptSwallowedFailure,
				Reason:    openErr.Error(),
				DetailErr: openErr,
			}, diag.AttrOpts{CallID: in.traceID})
			e.appendIndependentTerminalLeg(in.outCtx, in.billingState, in.aLegID, bleg, cand.Primary, e.now(), e.now(), billing.LegOutcomeNeverStarted)
			excluded[cand.Key] = struct{}{}
			continue
		}

		legCtx, legCancel := context.WithCancel(launchCtx)

		launched = append(launched, wireParallelLeg{
			cand:       cand,
			bleg:       bleg,
			permit:     permit,
			launchCtx:  legCtx,
			cancel:     legCancel,
			bodyReader: bodyReader,
			contentLen: contentLen,
			startedAt:  e.now(),
		})
	}

	if len(launched) == 0 {
		if budget.usedNow() >= budget.max {
			return wireParallelRaceResult{}, fmt.Errorf("executor: %w", lipapi.ErrMaxRouteAttempts)
		}
		return wireParallelRaceResult{opened: false}, nil
	}

	type outcomeMsg struct {
		idx    int
		stream lipapi.ManagedEventStream
		err    error
	}

	ch := make(chan outcomeMsg, len(launched))
	var wg sync.WaitGroup

	for i := range launched {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			leg := launched[idx]

			be, beOK := e.Backends[leg.cand.Primary.Backend]
			if !beOK {
				_ = leg.bodyReader.Close()
				leg.cancel()
				ch <- outcomeMsg{idx: idx, err: fmt.Errorf("backend %q not configured", leg.cand.Primary.Backend)}
				return
			}

			// Apply TTFT scoped context to worker
			openCtx, cancelOpen, ttftDeadline := ttft.scopedContext(leg.launchCtx, e.now(), leg.cand.Key, leg.cand.Primary.TTFTTimeout)
			var stopWatcher func() bool
			if ttftDeadline.scope != ttftTimeoutNone {
				stopWatcher = context.AfterFunc(openCtx, leg.cancel)
			}
			defer cancelOpen()

			// BE-ingress checkpoint (Finding 3 & Suggestion 5)
			if holder := meteringHolderFrom(in.outCtx); holder != nil {
				_, ingErr := e.CaptureWireBackendIngress(openCtx, holder, WireBackendIngressArgs{
					RequestID:       in.requestID,
					TraceID:         in.traceID,
					AttemptID:       leg.bleg.BLegID,
					BLegID:          leg.bleg.BLegID,
					ALegID:          in.aLegID,
					SessionID:       in.sessionID,
					Scope:           scope.PrincipalScopeView{},
					BackendID:       leg.cand.Primary.Backend,
					Model:           leg.cand.Primary.Model,
					MaxOutputTokens: in.maxOutputTokens,
					Now:             leg.startedAt,
					SourceDigest:    in.turnFacts.Source.SourceDigest.Sum(),
				})
				if ingErr != nil {
					if stopWatcher != nil {
						stopWatcher()
					}
					_ = leg.bodyReader.Close()
					leg.cancel()
					ch <- outcomeMsg{idx: idx, err: ingErr}
					return
				}
			}

			wireReq := largebody.WireOpenRequest{
				Candidate:     leg.cand,
				Body:          leg.bodyReader,
				ContentLength: leg.contentLen,
				WireRequest: largebody.WireRequestFacts{
					ProfileID:       in.turnFacts.Route.ProfileID,
					Operation:       in.turnFacts.Protocol.Operation,
					Delivery:        in.turnFacts.Protocol.Delivery,
					BodyMode:        in.turnFacts.Source.BodyMode,
					Rewrite:         in.turnFacts.Rewrite.Semantics,
					ClientModel:     in.turnFacts.Route.ClientModel,
					CandidateModel:  leg.cand.Primary.Model,
					MaxOutputTokens: in.turnFacts.MaxOutput.MaxOutputTokens,
				},
				TraceID: in.traceID,
				ALegID:  in.aLegID,
				BLegID:  leg.bleg.BLegID,
			}

			stream, err := execbackend.EffectiveWireOpen(leg.launchCtx, be, wireReq)
			if err != nil {
				if stopWatcher != nil {
					stopWatcher()
				}
				_ = leg.bodyReader.Close()
				leg.cancel()
				ch <- outcomeMsg{idx: idx, err: err}
				return
			}

			peeked, _, err := peekFirstWireEvent(openCtx, stream)
			if err != nil {
				if stopWatcher != nil {
					stopWatcher()
				}
				_ = leg.bodyReader.Close()
				leg.cancel()
				ch <- outcomeMsg{idx: idx, err: err}
				return
			}

			if stopWatcher != nil {
				stopWatcher()
			}
			// Winner candidate reached first event - do NOT cancel leg.launchCtx!
			ch <- outcomeMsg{idx: idx, stream: peeked}
		}(i)
	}

	// Close ch when all goroutines finish
	go func() {
		wg.Wait()
		close(ch)
	}()

	var winner *outcomeMsg
	var loserStreams []lipapi.ManagedEventStream
	var firstArmErr error

	for res := range ch {
		if winner == nil && res.err == nil && res.stream != nil {
			// Winner found!
			w := res
			winner = &w
			// Cancel other legs promptly
			for j, l := range launched {
				if j != res.idx {
					l.cancel()
				}
			}
		} else {
			// Loser or error
			if res.stream != nil {
				loserStreams = append(loserStreams, res.stream)
			}
			leg := launched[res.idx]
			if leg.bodyReader != nil {
				_ = leg.bodyReader.Close()
			}
			leg.permit.Abort()
			leg.cancel() // Cancel loser
			reason := "lost parallel race"
			outcomeKind := lipapi.AttemptCancelled
			terminalOutcome := billing.LegOutcomeCanceled
			var detailErr error
			isLoserCancellation := winner != nil && errors.Is(res.err, context.Canceled) && (in.outCtx == nil || in.outCtx.Err() == nil)
			if res.err != nil && !isLoserCancellation {
				if firstArmErr == nil {
					firstArmErr = res.err
				}
				reason = attemptReasonDetail(res.err)
				outcomeKind = lipapi.AttemptSwallowedFailure
				terminalOutcome = billing.LegOutcomeFailed
				detailErr = res.err
				excluded[leg.cand.Key] = struct{}{}
			}
			e.recordAttemptLogged(in.outCtx, recordAttemptParams{
				ALegID:    in.aLegID,
				BLeg:      leg.bleg,
				Cand:      leg.cand,
				Outcome:   outcomeKind,
				Reason:    reason,
				DetailErr: detailErr,
			}, diag.AttrOpts{CallID: in.traceID})
			e.appendIndependentTerminalLeg(in.outCtx, in.billingState, in.aLegID, leg.bleg, leg.cand.Primary, leg.startedAt, e.now(), terminalOutcome)
		}
	}

	for _, s := range loserStreams {
		_ = s.Close()
	}

	if winner == nil {
		if failures != nil && failures.ParallelFailure == nil && firstArmErr != nil {
			failures.ParallelFailure = fmt.Errorf("executor: parallel race arm failed: %w", firstArmErr)
		}
		return wireParallelRaceResult{opened: false}, nil
	}

	winnerLeg := launched[winner.idx]
	ready := wireReadyAttempt{stream: winner.stream}
	commitRes, commitErr := winnerLeg.permit.Commit(ready.lifecycleHandle())
	if commitRes.Canceled || errors.Is(commitErr, leglifecycle.ErrALegCanceled) {
		_ = winner.stream.Close()
		winnerLeg.cancel()
		winnerLeg.permit.Abort()
		return wireParallelRaceResult{}, leglifecycle.ErrALegCanceled
	}
	if commitErr != nil {
		_ = winner.stream.Close()
		winnerLeg.cancel()
		return wireParallelRaceResult{}, commitErr
	}

	ttft.markCommitted()

	e.recordAttemptLogged(in.outCtx, recordAttemptParams{
		ALegID:  in.aLegID,
		BLeg:    winnerLeg.bleg,
		Cand:    winnerLeg.cand,
		Outcome: lipapi.AttemptSuccess,
	}, diag.AttrOpts{CallID: in.traceID})

	if affinityKeyOK && affinityKey.Valid() && e.AffinityStore != nil {
		binding := affinity.BindingFromCandidate(affinityKey, winnerLeg.cand, e.now(), "output_committed")
		_ = e.AffinityStore.Set(context.WithoutCancel(in.outCtx), binding)
		e.noteRouteDecision(context.WithoutCancel(in.outCtx), in.traceID, "affinity_bind", binding.BackendID)
	}

	return wireParallelRaceResult{
		opened:     true,
		stream:     winner.stream,
		cand:       winnerLeg.cand,
		bleg:       winnerLeg.bleg,
		startedAt:  winnerLeg.startedAt,
		cancel:     winnerLeg.cancel,
		bodyCloser: winnerLeg.bodyReader,
	}, nil
}
