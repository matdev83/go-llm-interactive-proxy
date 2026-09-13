package runtime

import (
	"strings"
	"time"

	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// localBoundaryCaptureEnabled reports whether this executor has a local
// observation consumer. The accumulator is request/B-leg scoped and is never
// allocated for ordinary no-accounting execution.
func (e *Executor) localBoundaryCaptureEnabled() bool {
	return e != nil && (e.MeteringRecorder != nil || e.BillingLegObserver != nil || e.TerminalUsageSink != nil)
}

// localBoundaryCaptureRequired reports whether the canonical path must retain
// final-boundary evidence before a provider fast lane may commit bytes.
func (e *Executor) localBoundaryCaptureRequired() bool {
	// The durable Phase 3 recorder is the explicit required local-evidence
	// capability. Terminal billing sinks remain compatible with the wire lane;
	// their attempt-owned records receive local observations when available,
	// while the optional observer/no-op test sink must not alter eligibility.
	return e != nil && e.MeteringRecorder != nil
}

func (e *Executor) newLocalBoundaryAccumulator() *coremetering.BoundaryAccumulator {
	if !e.localBoundaryCaptureEnabled() {
		return nil
	}
	return coremetering.NewBoundaryAccumulator()
}

func (a *attemptSession) drainLocalBoundaryObservations(now time.Time) []metering.Observation {
	if a == nil {
		return nil
	}
	a.billingMu.Lock()
	if a.boundaryDrained {
		a.billingMu.Unlock()
		return nil
	}
	a.boundaryDrained = true
	boundary := a.boundary
	identity := coremetering.ObservationIdentity{
		StoreID:       strings.TrimSpace(a.billingStoreID),
		RequestID:     strings.TrimSpace(a.requestID),
		CallID:        a.billingCallID.String(),
		BillingCallID: a.billingCallID.String(),
		ALegID:        strings.TrimSpace(a.bleg.ALegID),
		BLegID:        strings.TrimSpace(a.bleg.BLegID),
		AttemptID:     strings.TrimSpace(a.bleg.BLegID),
		AttemptSeq:    uint64(maxInt(a.bleg.Seq, 0)),
		Scope:         a.boundaryScope.Clone(),
		ObservedAt:    now,
		ReceivedAt:    now,
	}
	a.billingMu.Unlock()
	if boundary == nil {
		return nil
	}
	return boundary.Observations(identity)
}

func (a *attemptSession) observeLocalProviderEvent(event lipapi.Event, media ...coremetering.MediaSummary) {
	if a != nil && a.boundary != nil {
		a.boundary.ObserveProviderEvent(event, media...)
	}
}

func (a *attemptSession) observeLocalCustomerEvent(event lipapi.Event, media ...coremetering.MediaSummary) {
	if a != nil && a.boundary != nil {
		a.boundary.ObserveCustomerEvent(event, media...)
	}
}

func maxInt(value, floor int) int {
	if value < floor {
		return floor
	}
	return value
}
