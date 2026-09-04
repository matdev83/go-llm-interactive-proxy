package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	coretraffic "github.com/matdev83/go-llm-interactive-proxy/internal/core/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

// responseEventInput is intentionally a narrow per-event boundary. It carries
// only the immutable request facts/current attempt and observer dependencies
// needed for one emitted response event; it is not a general runtime bag.
type responseEventInput struct {
	facts     recvTurnFacts
	attempt   *attemptSession
	recovery  *recoveryController
	pm        sdk.PartMeta
	committed bool
	now       time.Time

	// recorded is true for response_finished paths that ran mandatory recorder
	// preflight before authority finalization.
	recorded            bool
	finishBeforeRelease bool
	finishAfterRemember bool
}

// recvEventPreparation is the response-side portion of a backend event. It
// carries only values back to Recv; terminal and recovery decisions remain at
// that explicit coordination boundary.
type recvEventPreparation struct {
	event          lipapi.Event
	partMeta       sdk.PartMeta
	toolMeta       sdk.ToolMeta
	isTool         bool
	sourceID       string
	sourceFinished bool
	swallowed      bool
	err            error
}

// prepareRecvEvent accounts for and enriches one backend event. Attempt-local
// accounting is updated through the explicit attempt value, while response
// evidence and hooks stay owned by this pipeline.
func (p *responsePipeline) prepareRecvEvent(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev lipapi.Event) recvEventPreparation {
	prepared := recvEventPreparation{event: ev}
	if p == nil || attempt == nil {
		prepared.err = errNilRetryRecvStream
		return prepared
	}
	at := p.nowTime()
	attempt.observeAccountingBackendEvent(at, ev)
	if ev.Kind == lipapi.EventUsageDelta && ev.Accounting.DedupeKey != "" && !attempt.rememberUsageEvidenceOnce(ev) {
		prepared.swallowed = true
		return prepared
	}
	attempt.observeAccountingUsage(ev)
	prepared.partMeta, _ = facts.hookMeta(attempt.bleg, attempt.cand)
	p.emitTraffic(ctx, attempt, sdktraffic.LegBTP, ev, prepared.partMeta)
	p.emitUsage(ctx, facts, attempt, ev)
	if toolFinal := attempt.toolCallAssembler(); toolFinal != nil && toolFinal.enabled() {
		meta := toolcall.Meta{TraceID: facts.traceID, ALegID: facts.aLegID, BLegID: attempt.bleg.BLegID, AttemptSeq: attempt.bleg.Seq}
		held, err := toolFinal.ingest(ctx, ev, meta)
		if err != nil {
			p.clearToolClassification()
			prepared.err = err
			return prepared
		}
		if held {
			prepared.swallowed = true
			return prepared
		}
	}
	return prepared
}

// clientEventTransformation applies response-side tool policy/reactors and
// hooks. Gate buffering is returned as data so Recv can sequence
// terminal/recovery ownership explicitly.
type clientEventTransformation struct {
	event          lipapi.Event
	partMeta       sdk.PartMeta
	toolMeta       sdk.ToolMeta
	isTool         bool
	sourceID       string
	sourceFinished bool
	gates          []completion.Gate
	swallowed      bool
	err            error
}

type gatedEventResult struct {
	event           lipapi.Event
	replaced        bool
	finishPreflight bool
	recording       responseRecordingResult
	err             error
}

// applyCompletionGates owns gate buffering and response-side preflight. It
// deliberately returns terminal work as data for Recv to sequence.
func (p *responsePipeline) applyCompletionGates(ctx context.Context, gates []completion.Gate, facts recvTurnFacts, attempt *attemptSession, ev lipapi.Event, committed bool) gatedEventResult {
	result := gatedEventResult{}
	if p == nil || attempt == nil {
		result.err = errNilRetryRecvStream
		return result
	}
	snap := p.completionSnapshot(ctx)
	meta := completion.Meta{TraceID: facts.traceID, ALegID: facts.aLegID, BLegID: attempt.bleg.BLegID, AttemptSeq: attempt.bleg.Seq}
	if views, ok := facts.viewsFor(ctx); ok {
		meta.Scope, meta.Session, meta.Workspace = views.Scope, views.Session, views.Workspace
	}
	services := completion.Services{}
	if snap != nil {
		services.State, services.Aux = snap.State(), snap.Aux()
	}
	out, replaced, err := p.completionGatedEmit(ctx, gates, ev, responseGateInput{
		meta: meta, services: services, stageLog: p.log, committed: committed, limits: completionBufferLimitsFor(p),
	})
	if errors.Is(err, errGateContinueInner) {
		result.finishPreflight = false
		result.err = errGateContinueInner
		return result
	}
	if err != nil {
		result.err = err
		return result
	}
	result.event, result.replaced = out, replaced
	if replaced {
		views, ok := facts.viewsFor(ctx)
		if err := p.cycleFinalStreamObservation(ctx, facts, attempt, views, ok, response.OutcomeGateReplaced, committed); err != nil {
			result.err = err
			return result
		}
	}
	if out.Kind == lipapi.EventResponseFinished {
		result.recording = p.recordClientFacing(ctx, facts, attempt, out, committed)
		if result.recording.mandatory() {
			p.finishFinalStreamObservation(ctx, attempt, response.OutcomeFailed)
			result.err = result.recording.err
			return result
		}
		result.finishPreflight = true
	}
	return result
}

func (p *responsePipeline) transformClientEvent(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev lipapi.Event, prepared recvEventPreparation) clientEventTransformation {
	out := clientEventTransformation{event: ev, partMeta: prepared.partMeta, toolMeta: prepared.toolMeta, sourceID: prepared.sourceID, sourceFinished: prepared.sourceFinished, err: prepared.err, swallowed: prepared.swallowed}
	if out.err != nil || out.swallowed || p == nil || attempt == nil {
		return out
	}
	pm, tm := facts.hookMeta(attempt.bleg, attempt.cand)
	out.partMeta, out.toolMeta = pm, tm
	if te, ok := lipapi.ToolEventFromEvent(ev); ok {
		out.isTool = true
		out.sourceID = te.ToolCallID
		out.sourceFinished = te.Kind == lipapi.ToolEventFinished
		p.enrichToolEvent(&te)
		if err := p.applyToolPolicies(ctx, facts, te, tm); err != nil {
			out.err = err
			return out
		}
		res := p.bus.ApplyToolReactors(ctx, te, tm)
		if res.Err != nil {
			out.err = res.Err
			return out
		}
		if !res.Emit {
			if te.Kind == lipapi.ToolEventFinished {
				p.forgetToolClassification(te.ToolCallID)
			}
			out.swallowed = true
			return out
		}
		p.rememberEffectiveTool(te.ToolCallID, res.Event)
		if res.Event.Kind != "" {
			out.event = lipapi.MergeToolEventInto(ev, res.Event)
		}
	}
	if err := p.bus.RunResponsePartHooks(ctx, &out.event, pm); err != nil {
		out.err = err
		return out
	}
	if out.isTool {
		p.observeToolFinalName(out.sourceID, out.event)
		if out.sourceFinished {
			p.forgetToolClassification(out.sourceID)
		}
	}
	out.gates = p.completionGatesFromContext(ctx)
	return out
}

// observeClientFacing runs the response-owned observation/release sequence.
// It never mutates terminal authority; callers apply terminal/recovery policy
// from the returned recording result and error.
func (p *responsePipeline) observeClientFacing(ctx context.Context, ev lipapi.Event, in responseEventInput) (lipapi.Event, responseRecordingResult, error) {
	if p == nil {
		return lipapi.Event{}, responseRecordingResult{}, errNilRetryRecvStream
	}
	if in.attempt == nil {
		return lipapi.Event{}, responseRecordingResult{}, errNilRetryRecvStream
	}
	if err := extensions.RunFinalStreamObservationStage(ctx, p.log, p.extensionMetrics, in.attempt.finalStreamObs, ev, in.committed); err != nil {
		p.finishFinalStreamObservation(ctx, in.attempt, response.OutcomeFailed)
		return lipapi.Event{}, responseRecordingResult{}, err
	}

	recording := responseRecordingResult{outcome: responseRecordingSkipped}
	if !in.recorded {
		recording = p.recordClientFacing(ctx, in.facts, in.attempt, ev, in.committed)
		if recording.failed() && recording.mandatory() {
			p.finishFinalStreamObservation(ctx, in.attempt, response.OutcomeFailed)
			return lipapi.Event{}, recording, recording.err
		}
		if recording.err != nil && p.log != nil {
			p.log.DebugContext(ctx, "secure_session recorder stream", "error", recording.err)
		}
	}

	releaseDispatch := p.emitTrafficPTCFinal(ctx, in.facts, in.attempt, &ev, in.pm)
	p.rememberClientEvent(ev)
	p.commitAffinityIfOutput(ctx, in.recovery, in.facts.terminalFacts(), in.attempt, in.now, ev)
	p.notifyCompactionAfterRelease(ctx, ev, releaseDispatch)
	return ev, recording, nil
}

func (p *responsePipeline) observeSynthesizedUsage(ctx context.Context, ev lipapi.Event, request requestTerminalFacts, attempt *attemptSession, pm sdk.PartMeta, committed bool) (lipapi.Event, responseRecordingResult, error) {
	if p == nil || attempt == nil {
		return lipapi.Event{}, responseRecordingResult{}, errNilRetryRecvStream
	}
	ctx = request.toRecvTurnFacts(ctx).projectContext(ctx, p.log)
	if err := extensions.RunFinalStreamObservationStage(ctx, p.log, p.extensionMetrics, attempt.finalStreamObs, ev, committed); err != nil {
		p.finishFinalStreamObservation(ctx, attempt, response.OutcomeFailed)
		return lipapi.Event{}, responseRecordingResult{}, err
	}
	recording := p.recordClientFacingTerminal(ctx, request, attempt, ev, committed)
	if recording.failed() && recording.mandatory() {
		p.finishFinalStreamObservation(ctx, attempt, response.OutcomeFailed)
		return lipapi.Event{}, recording, recording.err
	}
	releaseDispatch := p.emitTrafficPTCFinalEvidence(ctx, request.responseEvidence(), attempt, &ev, pm)
	p.rememberClientEvent(ev)
	p.notifyCompactionAfterRelease(ctx, ev, releaseDispatch)
	return ev, recording, nil
}

func (p *responsePipeline) commitAffinityIfOutput(ctx context.Context, recovery *recoveryController, request requestTerminalFacts, attempt *attemptSession, now time.Time, ev lipapi.Event) {
	if lipapi.OutputCommitted(ev) && recovery != nil {
		if now.IsZero() {
			now = time.Now()
		}
		recovery.commitAffinity(ctx, request, attempt, now, "output_committed")
	}
}

func (p *responsePipeline) emitTrafficPTCFinal(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev *lipapi.Event, pm sdk.PartMeta) compactionReleaseDispatch {
	return p.emitTrafficPTCFinalEvidence(ctx, responseRequestEvidence{traceID: facts.traceID, aLegID: facts.aLegID, sessionID: facts.baseline.Session.AuthoritativeSessionID}, attempt, ev, pm)
}

func (p *responsePipeline) emitTrafficPTCFinalEvidence(ctx context.Context, evidence responseRequestEvidence, attempt *attemptSession, ev *lipapi.Event, pm sdk.PartMeta) compactionReleaseDispatch {
	if ev == nil {
		return compactionReleaseDispatch{}
	}
	if ev.Kind == lipapi.EventWarning && ev.WarningCode == stream.KeepaliveEventCode {
		return compactionReleaseDispatch{}
	}
	dispatch := p.observeCompactionReleaseFinalEvidence(ctx, evidence, attempt, ev)
	p.emitTraffic(ctx, attempt, sdktraffic.LegPTC, *ev, pm)
	return dispatch
}

func (p *responsePipeline) emitTraffic(ctx context.Context, attempt *attemptSession, leg sdktraffic.Leg, ev lipapi.Event, pm sdk.PartMeta) {
	if p == nil || p.runtimeSnapshot == nil || attempt == nil {
		return
	}
	bundle := coretraffic.PortBundleFromSnapshot(p.runtimeSnapshot)
	if bundle.EmitIsNoop() {
		return
	}
	b, err := json.Marshal(ev)
	if err != nil {
		if p.log != nil {
			p.log.DebugContext(ctx, "response pipeline traffic marshal skipped", "leg", leg, "error", err)
		}
		return
	}
	sc := scopeFromCtx(ctx)
	meta := sdktraffic.CaptureMeta{
		TraceID:     pm.TraceID,
		ALegID:      pm.ALegID,
		BLegID:      pm.BLegID,
		AttemptSeq:  pm.AttemptSeq,
		BackendID:   strings.TrimSpace(attempt.cand.Primary.Backend),
		PrincipalID: strings.TrimSpace(sc.PrincipalID.String()),
		Scope:       sc,
	}
	bundle.Emit(ctx, leg, meta, "lip/canonical+json", "application/json", b)
}

// emitUsage observes provider/customer usage without importing settlement or
// terminal authority. Attempt identity is explicit so replacement evidence
// cannot be attributed to the current slot accidentally.
func (p *responsePipeline) emitUsage(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev lipapi.Event) {
	p.emitUsageEvidence(ctx, responseRequestEvidence{traceID: facts.traceID, aLegID: facts.aLegID, secureTurn: facts.secureTurn}, facts.baseline.Session.CorrelationID(), attempt, ev)
}

func (p *responsePipeline) emitUsageTerminal(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, ev lipapi.Event) {
	p.emitUsageEvidence(ctx, request.responseEvidence(), request.call.Session.CorrelationID(), attempt, ev)
}

func (p *responsePipeline) emitUsageEvidence(ctx context.Context, evidence responseRequestEvidence, sessionID string, attempt *attemptSession, ev lipapi.Event) {
	if p == nil || p.runtimeSnapshot == nil || ev.Kind != lipapi.EventUsageDelta || attempt == nil {
		return
	}
	obs := p.runtimeSnapshot.UsageObserver()
	if obs == nil {
		return
	}
	scopeView := scopeFromCtx(ctx)
	principalID := ""
	if scopeView.PrincipalID.IsKnown() {
		principalID = strings.TrimSpace(scopeView.PrincipalID.String())
	}
	if err := obs.OnUsage(ctx, usage.Event{
		TraceID: strings.TrimSpace(evidence.traceID), ALegID: strings.TrimSpace(evidence.aLegID),
		BLegID: strings.TrimSpace(attempt.bleg.BLegID), PrincipalID: principalID,
		SessionID: strings.TrimSpace(sessionID), AttemptSeq: int(attempt.bleg.Seq),
		BackendID: strings.TrimSpace(attempt.cand.Primary.Backend), Model: strings.TrimSpace(attempt.cand.Primary.Model),
		Scope: scopeView.Clone(), InputTokens: ev.InputTokens, OutputTokens: ev.OutputTokens,
		CacheReadTokens: ev.CacheReadTokens, CacheWriteTokens: ev.CacheWriteTokens,
		ReasoningTokens: ev.ReasoningTokens, TotalTokens: ev.TotalTokens,
		CostNanoUnits: ev.CostNanoUnits, Currency: strings.TrimSpace(ev.Currency),
		CostSource: strings.TrimSpace(ev.CostSource), RawUsageJSON: strings.TrimSpace(ev.RawUsageJSON),
		RecordedAt: p.nowTime(),
	}); err != nil && p.log != nil {
		p.log.DebugContext(ctx, "usage observer error", "error", err)
	}
}

// consumeBackendUsageEvidenceForAttempt keeps provider sideband evidence
// attached to the attempt that produced the source stream. The response owner
// records and emits the evidence; the attempt owns dedupe and accounting.
func (p *responsePipeline) consumeBackendUsageEvidenceForAttempt(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, inner lipapi.ManagedEventStream) {
	if p == nil || attempt == nil || inner == nil {
		return
	}
	source, ok := inner.(lipapi.UsageEvidenceSource)
	if !ok {
		return
	}
	for _, ev := range source.DrainUsageEvidence() {
		if ev.Kind != lipapi.EventUsageDelta || !attempt.rememberUsageEvidenceOnce(ev) {
			continue
		}
		p.rememberInternalUsage(ev)
		attempt.observeAccountingUsage(ev)
		p.emitUsage(ctx, facts, attempt, ev)
	}
}

func (p *responsePipeline) nowTime() time.Time {
	if p != nil && p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p *responsePipeline) keepaliveEvent() lipapi.Event {
	return stream.DefaultKeepaliveEvent()
}

// withDecisionEvidence projects the response-stage evidence seam into a
// receive context. Terminal commitment remains owned by turnTerminal.
func (p *responsePipeline) withDecisionEvidence(ctx context.Context, terminal *turnTerminal) context.Context {
	if p == nil || p.runtimeSnapshot == nil {
		return ctx
	}
	snap := p.runtimeSnapshot
	emitter := p.policyEvidenceEmitter(snap)
	ev := &extensions.DecisionEvidence{
		Emitter:               emitter,
		TimeoutBudget:         snap.TimeoutBudgetSource(),
		TimeoutGuard:          snap.ProviderTimeoutGuard(),
		OutputCommittedSource: func() bool { return terminal != nil && terminal.committed() },
	}
	ctx = extensions.WithDecisionEvidence(ctx, ev)
	ctx = hooks.WithToolReactorEvidence(ctx, extensions.NewToolReactorEvidenceFunc(ev))
	ctx = extensions.WithAttemptEvidence(ctx, extensions.NewAttemptEvidenceFunc(ev))
	return ctx
}

func (p *responsePipeline) completionSnapshot(ctx context.Context) *extensions.RequestRuntimeSnapshot {
	if snap := extensions.RequestRuntimeSnapshotFromContext(ctx); snap != nil {
		return snap
	}
	if p != nil {
		return p.runtimeSnapshot
	}
	return nil
}

func (p *responsePipeline) completionGatesFromContext(ctx context.Context) []completion.Gate {
	var fallback extensions.CompletionGatesView
	if p != nil {
		fallback = p.runtimeSnapshot
	}
	return extensions.CompletionGatesFromContext(ctx, fallback)
}

func (p *responsePipeline) applyToolPolicies(ctx context.Context, facts recvTurnFacts, te lipapi.ToolEvent, meta sdk.ToolMeta) error {
	if p == nil || p.runtimeSnapshot == nil {
		return nil
	}
	policies := p.runtimeSnapshot.ToolCallPoliciesExecution()
	if len(policies) == 0 {
		return nil
	}
	polMeta := toolpolicy.Meta{
		TraceID: strings.TrimSpace(meta.TraceID), ALegID: strings.TrimSpace(meta.ALegID),
		BLegID: strings.TrimSpace(meta.BLegID), AttemptSeq: meta.AttemptSeq,
		Principal: meta.Principal, Scope: meta.Scope, Session: meta.Session, Workspace: meta.Workspace,
	}
	if v, ok := facts.viewsFor(ctx); ok {
		polMeta.Principal, polMeta.Scope, polMeta.Session, polMeta.Workspace = v.Principal, v.Scope, v.Session, v.Workspace
	}
	return extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx: ctx, Log: p.log, Obs: p.extensionMetrics, Policies: policies, Event: te, Meta: polMeta,
		Svc: toolpolicy.Services{State: p.runtimeSnapshot.State(), Aux: p.runtimeSnapshot.Aux()},
	})
}
