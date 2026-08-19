package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	coretraffic "github.com/matdev83/go-llm-interactive-proxy/internal/core/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
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

	if in.finishBeforeRelease && ev.Kind == lipapi.EventResponseFinished {
		p.finishFinalStreamObservation(ctx, in.attempt, response.OutcomeSuccessReleased)
	}
	releaseDispatch := p.emitTrafficPTCFinal(ctx, in.facts, in.attempt, &ev, in.pm)
	p.rememberClientEvent(ev)
	if in.finishAfterRemember && ev.Kind == lipapi.EventResponseFinished {
		p.finishFinalStreamObservation(ctx, in.attempt, response.OutcomeSuccessReleased)
	}
	p.commitAffinityIfOutput(ctx, in.recovery, in.facts, in.attempt, in.now, ev)
	p.notifyCompactionAfterRelease(ctx, ev, releaseDispatch)
	return ev, recording, nil
}

func (p *responsePipeline) commitAffinityIfOutput(ctx context.Context, recovery *recoveryController, facts recvTurnFacts, attempt *attemptSession, now time.Time, ev lipapi.Event) {
	if lipapi.OutputCommitted(ev) && recovery != nil {
		if now.IsZero() {
			now = time.Now()
		}
		recovery.commitAffinity(ctx, facts, attempt, now, "output_committed")
	}
}

func (p *responsePipeline) emitTrafficPTCFinal(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev *lipapi.Event, pm sdk.PartMeta) compactionReleaseDispatch {
	if ev == nil {
		return compactionReleaseDispatch{}
	}
	if ev.Kind == lipapi.EventWarning && ev.WarningCode == stream.KeepaliveEventCode {
		return compactionReleaseDispatch{}
	}
	dispatch := p.observeCompactionReleaseFinal(ctx, facts, attempt, ev)
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
		TraceID: strings.TrimSpace(facts.traceID), ALegID: strings.TrimSpace(facts.aLegID),
		BLegID: strings.TrimSpace(attempt.bleg.BLegID), PrincipalID: principalID,
		SessionID: strings.TrimSpace(facts.baseline.Session.CorrelationID()), AttemptSeq: int(attempt.bleg.Seq),
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

func (p *responsePipeline) nowTime() time.Time {
	if p != nil && p.now != nil {
		return p.now()
	}
	return time.Now()
}
