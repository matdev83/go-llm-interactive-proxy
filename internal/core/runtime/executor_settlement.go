package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	accountingledger "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/ledger"
	accountingobs "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/observability"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

// persistCancellationBilling records the final usage/cost evidence for a canceled
// attempt and settles the matching usage authority reservation. When a backend
// EventUsageDelta was already observed mid-stream (accounting.usageObserved), the
// observed usage is settled directly as a Cancellation — no estimated billing marker
// is needed. Otherwise it first attempts the backend's FinalizeBilling hook (when
// present) to recover authoritative usage; on failure it records an estimated
// billing marker so accounting still has evidence. Every path settles the
// reservation via settleCancellationAuthority, which is a no-op when the reservation
// is already settled and releases with ReleaseKindLosing when the settle fails.
func (s *retryRecvStream) persistCancellationBilling(ctx context.Context, reason string) {
	if s == nil {
		return
	}
	if s.accounting.usageObserved {
		s.settleCancellationAuthority(ctx)
		return
	}
	if s.finalizeBillingAfterCancel(ctx, reason) {
		s.settleCancellationAuthority(ctx)
		return
	}
	s.recordCancellationBillingMarker(ctx, reason)
	s.settleCancellationAuthority(ctx)
}

// settleCancellationAuthority settles the usage-authority reservation for a canceled
// attempt with the observed usage as a Cancellation. It is a no-op when the
// reservation is already settled (preventing a double settle of a strict
// reservation, e.g. after a prior partial/final settle). The losing-fallback
// (ReleaseKindLosing when the settle fails) now lives inside the authorityLifecycle
// owner's Settle, mirroring the finalizeResponseFinishedAuthority path.
func (s *retryRecvStream) settleCancellationAuthority(ctx context.Context) {
	if s == nil || s.authority.Settled() {
		return
	}
	s.authority.Settle(ctx, authorityapp.SettlementKindCancellation, mergeUsageEvents(tokenAccountingUsageEvents(s.seenEvents)), true)
}

func (s *retryRecvStream) recordCancellationBillingMarker(ctx context.Context, reason string) {
	if s == nil || s.accounting.usageObserved {
		return
	}
	raw, _ := json.Marshal(map[string]any{
		"billing_basis": "estimated_after_a_leg_cancellation",
		"reason":        strings.TrimSpace(reason),
	})
	ev := lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		CostSource:   accounting.CostSourceEstimated,
		RawUsageJSON: string(raw),
	}
	persistCtx := context.WithoutCancel(ctx)
	if err := s.beforeEmitClientFacing(persistCtx, ev); err != nil && s.executor != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(persistCtx, "secure_session cancellation billing marker", "error", err)
	}
	s.emitUsage(persistCtx, ev)
}

func (s *retryRecvStream) finalizeBillingAfterCancel(ctx context.Context, reason string) bool {
	if s == nil || s.executor == nil || s.executor.Backends == nil {
		return false
	}
	be, ok := s.executor.Backends[strings.TrimSpace(s.cand.Primary.Backend)]
	if !ok || be.FinalizeBilling == nil {
		return false
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	ev, err := be.FinalizeBilling(persistCtx, execbackend.BillingFinalizationInput{
		TraceID: strings.TrimSpace(s.traceID),
		ALegID:  strings.TrimSpace(s.aLegID),
		BLegID:  strings.TrimSpace(s.bleg.BLegID),
		Backend: strings.TrimSpace(s.cand.Primary.Backend),
		Model:   strings.TrimSpace(s.cand.Primary.Model),
		Reason:  strings.TrimSpace(reason),
	})
	if err != nil {
		if s.executor.Log != nil {
			s.executor.Log.DebugContext(persistCtx, "billing finalizer after cancellation", "error", err)
		}
		return false
	}
	if ev.Kind != lipapi.EventUsageDelta {
		return false
	}
	s.accounting.observeUsage(ev)
	if recErr := s.beforeEmitClientFacing(persistCtx, ev); recErr != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(persistCtx, "secure_session billing finalizer marker", "error", recErr)
	}
	s.emitUsage(persistCtx, ev)
	return true
}

func (s *retryRecvStream) emitUsage(ctx context.Context, ev lipapi.Event) {
	if s == nil || s.executor == nil || s.executor.RuntimeSnapshot == nil || ev.Kind != lipapi.EventUsageDelta {
		return
	}
	obs := s.executor.RuntimeSnapshot.UsageObserver()
	if obs == nil {
		return
	}
	principalID := ""
	scopeView := scopeFromCtx(ctx)
	if scopeView.PrincipalID.IsKnown() {
		principalID = strings.TrimSpace(scopeView.PrincipalID.String())
	}
	model := ""
	if s.cand.Primary.Model != "" {
		model = s.cand.Primary.Model
	}
	if err := obs.OnUsage(ctx, usage.Event{
		TraceID:          strings.TrimSpace(s.traceID),
		ALegID:           strings.TrimSpace(s.aLegID),
		BLegID:           strings.TrimSpace(s.bleg.BLegID),
		PrincipalID:      strings.TrimSpace(principalID),
		SessionID:        strings.TrimSpace(s.baseline.Session.CorrelationID()),
		AttemptSeq:       int(s.bleg.Seq),
		BackendID:        strings.TrimSpace(s.cand.Primary.Backend),
		Model:            strings.TrimSpace(model),
		Scope:            scopeView.Clone(),
		InputTokens:      ev.InputTokens,
		OutputTokens:     ev.OutputTokens,
		CacheReadTokens:  ev.CacheReadTokens,
		CacheWriteTokens: ev.CacheWriteTokens,
		ReasoningTokens:  ev.ReasoningTokens,
		TotalTokens:      ev.TotalTokens,
		CostNanoUnits:    ev.CostNanoUnits,
		Currency:         strings.TrimSpace(ev.Currency),
		CostSource:       strings.TrimSpace(ev.CostSource),
		RawUsageJSON:     strings.TrimSpace(ev.RawUsageJSON),
		RecordedAt:       s.executor.now(),
	}); err != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(ctx, "usage observer error", "error", err)
	}
}

func (s *retryRecvStream) emitSynthesizedUsage(ctx context.Context, ev lipapi.Event) (lipapi.Event, error) {
	s.accounting.observeClientEvent(s.now(), ev)
	if s.recoverPolicy != nil {
		s.recoverPolicy.ObserveClientEvent(ev, s.now())
	}
	s.rememberClientEvent(ev)
	if err := s.beforeEmitClientFacing(ctx, ev); err != nil {
		if s.executor != nil && s.executor.SecureSessionRecordingMandatory {
			return lipapi.Event{}, err
		}
		if s.executor != nil && s.executor.Log != nil {
			s.executor.Log.DebugContext(ctx, "secure_session recorder stream", "error", err)
		}
	}
	pm, _ := s.recvHookMeta()
	s.emitTrafficPTC(ctx, ev, pm)
	s.emitUsage(ctx, ev)
	return ev, nil
}

func (s *retryRecvStream) finalizeTokenAccounting(ctx context.Context, finish lipapi.Event) (lipapi.Event, bool, error) {
	if s == nil || s.executor == nil {
		return lipapi.Event{}, false, nil
	}
	if s.executor.StreamUsage == nil {
		s.authority.Settle(ctx, authorityapp.SettlementKindFinal, lipapi.Event{}, false)
		return lipapi.Event{}, false, nil
	}
	started := s.now()
	events := append([]lipapi.Event(nil), s.seenEvents...)
	events = append(events, finish)
	result, err := s.executor.StreamUsage.Reconstruct(ctx, accountingstream.Input{
		Backend:    strings.TrimSpace(s.cand.Primary.Backend),
		Model:      strings.TrimSpace(s.cand.Primary.Model),
		Call:       s.baseline,
		OutputText: s.visibleText.String(),
		Events:     events,
	})
	if err != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(ctx, "token accounting stream reconstruction", "error", err)
	}
	if len(result.Events) == 0 {
		s.authority.Settle(ctx, authorityapp.SettlementKindFinal, lipapi.Event{}, false)
		return lipapi.Event{}, false, nil
	}
	usageEv := mergeUsageEventsForClient(result.Events, tokenAccountingHasProviderUsage(s.seenEvents))
	duration := s.now().Sub(started)
	if duration <= 0 {
		duration = time.Nanosecond
	}
	if err := s.recordTokenAccountingLedger(ctx, result.Events, "", "", duration); err != nil {
		if s.executor.LedgerWriteRequired {
			s.authority.Settle(ctx, authorityapp.SettlementKindFinal, usageEv, false)
			return lipapi.Event{}, false, err
		}
	}
	s.authority.Settle(ctx, authorityapp.SettlementKindFinal, usageEv, false)
	return usageEv, true, nil
}

// finalizeResponseFinishedAuthority is the single authority-finalization chokepoint for
// response_finished completion paths. It runs token-accounting finalization, which settles
// the usage-authority reservation via the authorityLifecycle owner (the owner folds the
// losing-fallback release into Settle, so a failed settle releases ReleaseKindLosing and
// marks the lifecycle settled). Idempotent via tokenAccountingFinalized (which gates
// usage-delta re-queue, not authority idempotency — the owner owns that via settled). It
// does NOT mark the stream finished and does NOT queue the event — callers own
// emission/finish timing.
func (s *retryRecvStream) finalizeResponseFinishedAuthority(ctx context.Context, ev lipapi.Event) (lipapi.Event, bool, error) {
	if s.tokenAccountingFinalized {
		return lipapi.Event{}, false, nil
	}
	usageEv, ok, err := s.finalizeTokenAccounting(ctx, ev)
	if err != nil {
		return lipapi.Event{}, false, err
	}
	s.tokenAccountingFinalized = true
	return usageEv, ok, nil
}

func mergeUsageEvents(events []lipapi.Event) lipapi.Event {
	return mergeUsageEventsForClient(events, false)
}

func mergeUsageEventsForClient(events []lipapi.Event, skipProviderBillable bool) lipapi.Event {
	out := lipapi.Event{Kind: lipapi.EventUsageDelta, UsageScopes: []lipapi.ScopedUsageDelta{}}
	for _, ev := range events {
		if ev.Kind != lipapi.EventUsageDelta {
			continue
		}
		if len(ev.UsageScopes) > 0 {
			for _, scope := range ev.UsageScopes {
				if skipProviderBillable && scope.Accounting.Plane == lipapi.UsagePlaneProviderBillable {
					continue
				}
				out.UsageScopes = append(out.UsageScopes, scope)
			}
			continue
		}
		if skipProviderBillable && ev.Accounting.Plane == lipapi.UsagePlaneProviderBillable {
			continue
		}
		out.UsageScopes = append(out.UsageScopes, lipapi.ScopedUsageDelta{
			InputTokens:      ev.InputTokens,
			OutputTokens:     ev.OutputTokens,
			CacheReadTokens:  ev.CacheReadTokens,
			CacheWriteTokens: ev.CacheWriteTokens,
			ReasoningTokens:  ev.ReasoningTokens,
			TotalTokens:      ev.TotalTokens,
			Accounting:       ev.Accounting,
		})
	}
	if len(out.UsageScopes) > 0 {
		first := out.UsageScopes[0]
		out.InputTokens = first.InputTokens
		out.OutputTokens = first.OutputTokens
		out.CacheReadTokens = first.CacheReadTokens
		out.CacheWriteTokens = first.CacheWriteTokens
		out.ReasoningTokens = first.ReasoningTokens
		out.TotalTokens = first.TotalTokens
		out.Accounting = first.Accounting
	}
	return out
}

func tokenAccountingHasProviderUsage(events []lipapi.Event) bool {
	for _, ev := range events {
		if ev.Kind != lipapi.EventUsageDelta {
			continue
		}
		if ev.Accounting.Plane == lipapi.UsagePlaneProviderBillable {
			return true
		}
		for _, scope := range ev.UsageScopes {
			if scope.Accounting.Plane == lipapi.UsagePlaneProviderBillable {
				return true
			}
		}
	}
	return false
}

func (s *retryRecvStream) recordTokenAccountingLedger(ctx context.Context, events []lipapi.Event, unavailableReason, failureReason string, duration time.Duration) error {
	if s == nil || s.executor == nil || s.executor.Ledger == nil {
		return nil
	}
	for _, ev := range events {
		if ev.Kind != lipapi.EventUsageDelta {
			continue
		}
		scopes := ev.UsageScopes
		if len(scopes) == 0 {
			scopes = []lipapi.ScopedUsageDelta{{
				InputTokens:      ev.InputTokens,
				OutputTokens:     ev.OutputTokens,
				CacheReadTokens:  ev.CacheReadTokens,
				CacheWriteTokens: ev.CacheWriteTokens,
				ReasoningTokens:  ev.ReasoningTokens,
				TotalTokens:      ev.TotalTokens,
				Accounting:       ev.Accounting,
			}}
		}
		for _, scope := range scopes {
			if scope.Accounting.Plane == lipapi.UsagePlaneUnknown {
				continue
			}
			record := accountingledger.Record{
				RequestID:         strings.TrimSpace(s.baseline.ID),
				AttemptID:         strings.TrimSpace(s.bleg.BLegID),
				Backend:           strings.TrimSpace(s.cand.Primary.Backend),
				Model:             strings.TrimSpace(s.cand.Primary.Model),
				Plane:             scope.Accounting.Plane,
				InputTokens:       scope.InputTokens,
				OutputTokens:      scope.OutputTokens,
				CacheReadTokens:   scope.CacheReadTokens,
				CacheWriteTokens:  scope.CacheWriteTokens,
				ReasoningTokens:   scope.ReasoningTokens,
				TotalTokens:       scope.TotalTokens,
				Metadata:          scope.Accounting,
				CreatedAt:         s.now(),
				UnavailableReason: unavailableReason,
				FailureReason:     failureReason,
			}
			if record.RequestID == "" {
				record.RequestID = strings.TrimSpace(s.traceID)
			}
			if err := s.executor.Ledger.Record(ctx, record); err != nil {
				if s.executor.Log != nil {
					s.executor.Log.DebugContext(ctx, "token accounting ledger record", "error", err)
				}
				s.recordTokenAccountingObservation(ctx, record, err, duration)
				return err
			}
			s.recordTokenAccountingObservation(ctx, record, nil, duration)
		}
	}
	return nil
}

func (s *retryRecvStream) recordPartialTokenAccounting(ctx context.Context, reason string, err error) {
	s.recordPartialTokenAccountingLedger(ctx, reason, err)
	events := tokenAccountingUsageEvents(s.seenEvents)
	s.authority.Settle(ctx, authorityapp.SettlementKindPartial, mergeUsageEvents(events), false)
}

func (s *retryRecvStream) recordPartialTokenAccountingLedger(ctx context.Context, reason string, err error) {
	if s == nil || s.executor == nil || s.executor.Ledger == nil {
		return
	}
	events := tokenAccountingUsageEvents(s.seenEvents)
	if len(events) == 0 {
		return
	}
	duration := s.now().Sub(s.accounting.requestStartedAt)
	if duration <= 0 {
		duration = time.Nanosecond
	}
	_ = s.recordTokenAccountingLedger(ctx, events, reason, reason, duration)
}

func (s *retryRecvStream) recordTokenAccountingObservation(ctx context.Context, record accountingledger.Record, err error, duration time.Duration) {
	if s == nil || s.executor == nil || s.executor.TokenAccountingObservability == nil {
		return
	}
	obs, err := accountingobs.NewObservation(accountingobs.Input{
		Labels: accountingobs.Labels{
			Backend:   record.Backend,
			Model:     record.Model,
			Plane:     accountingobs.Plane(record.Plane),
			Source:    accountingobs.Source(record.Metadata.Source),
			Authority: accountingobs.Authority(record.Metadata.Authority),
		},
		Status:            observationStatus(record, err),
		UnavailableReason: record.UnavailableReason,
		Err:               err,
		Duration:          duration,
		OccurredAt:        record.CreatedAt,
	})
	if err != nil {
		if s.executor.Log != nil {
			s.executor.Log.DebugContext(ctx, "token accounting observation", "error", err)
		}
		return
	}
	s.executor.TokenAccountingObservability.Record(obs)
}

func observationStatus(record accountingledger.Record, err error) accountingobs.Status {
	if err != nil || record.FailureReason != "" {
		return accountingobs.StatusUnavailable
	}
	return accountingobs.StatusSuccess
}

func tokenAccountingUsageEvents(events []lipapi.Event) []lipapi.Event {
	out := []lipapi.Event{}
	for _, ev := range events {
		if ev.Kind == lipapi.EventUsageDelta {
			out = append(out, ev)
		}
	}
	return out
}

func (s *retryRecvStream) rememberClientEvent(ev lipapi.Event) {
	if s == nil {
		return
	}
	if ev.Kind == lipapi.EventResponseFinished {
		for _, seen := range s.seenEvents {
			if seen.Kind == lipapi.EventResponseFinished {
				return
			}
		}
	}
	if ev.Kind == lipapi.EventTextDelta {
		s.visibleText.WriteString(ev.Delta)
	}
	s.seenEvents = append(s.seenEvents, ev)
}
