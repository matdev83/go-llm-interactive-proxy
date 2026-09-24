package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Shadow V2 capture composition for Task 17.2 (Migration Strategy step 4).
//
// ComposeShadowV2Capture wires the explicit no-post shadow runner: V2
// observations, valuations, and reconciliation persist through pure ports
// while V1 remains the sole monetary writer. The input requires the V1
// settlement port to be present (coexistence proof) but the returned handle
// does not store or expose it, so shadow execution cannot address the V1
// writer either. No customer-unit ledger, provider-payable, adjustment, or
// journal writer appears in this file by design.
//
// Default ComposeBilling behavior is unchanged; stock lipstd startup stays
// non-money. No public pkg/lipruntime money option is added here.

// ErrShadowV2CaptureIncomplete identifies missing shadow capture ports.
var ErrShadowV2CaptureIncomplete = errors.New("runtimebundle: shadow V2 capture is incomplete")

// ShadowV2CaptureInput is the explicit typed shadow-mode wiring. Every pure
// port is required; V1Settlement proves the authoritative V1 writer remains
// bound while staying unreachable from the returned handle. Evidence capture
// requires the capture-only ShadowEvidenceSink: the ordinary terminal sink
// cannot satisfy this input, so shadow evidence can never become V1 posting
// or claim work through composition.
type ShadowV2CaptureInput struct {
	StoreID             string
	MaxObservations     int
	EvidenceSink        billing.ShadowEvidenceSink
	WorkAppender        billing.EconomicRevisionWorkAppender
	ResultStore         billing.EconomicRevisionResultStore
	ReconciliationStore billing.EconomicRevisionReconciliationStore
	Rater               billing.PostUsageRater
	V1Settlement        billing.CallSettlementStore
}

// ShadowV2Handle is the composed no-post shadow executor. It wraps only the
// pure billingstore runner; its static type exposes no monetary ledger.
type ShadowV2Handle struct {
	capture *billingstore.ShadowV2Capture
}

// ComposeShadowV2Capture validates explicit shadow wiring and returns a
// handle that can only persist immutable V2 evidence. Incomplete ports,
// unbounded work, missing store scope, or a missing V1 settlement fail closed
// before any capture runs.
func ComposeShadowV2Capture(in ShadowV2CaptureInput) (ShadowV2Handle, error) {
	if strings.TrimSpace(in.StoreID) == "" {
		return ShadowV2Handle{}, fmt.Errorf("%w: store scope is required", ErrShadowV2CaptureIncomplete)
	}
	if billing.IsNilPort(in.EvidenceSink) || billing.IsNilPort(in.WorkAppender) || billing.IsNilPort(in.ResultStore) ||
		billing.IsNilPort(in.ReconciliationStore) || billing.IsNilPort(in.Rater) {
		return ShadowV2Handle{}, fmt.Errorf("%w: evidence, work, result, reconciliation, and rater ports are required", ErrShadowV2CaptureIncomplete)
	}
	if billing.IsNilPort(in.V1Settlement) {
		return ShadowV2Handle{}, fmt.Errorf("%w: V1 settlement must remain bound as the sole monetary writer", ErrShadowV2CaptureIncomplete)
	}
	maxObservations := in.MaxObservations
	if maxObservations == 0 {
		maxObservations = 1024
	}
	capture, err := billingstore.NewShadowV2Capture(
		billingstore.ShadowV2CaptureConfig{StoreID: strings.TrimSpace(in.StoreID), MaxObservations: maxObservations},
		in.EvidenceSink, in.WorkAppender, in.ResultStore, in.ReconciliationStore, in.Rater,
	)
	if err != nil {
		return ShadowV2Handle{}, err
	}
	return ShadowV2Handle{capture: capture}, nil
}

// CaptureObservations persists immutable V2 observations without monetary
// effects and without creating usage rows or claim state.
func (h ShadowV2Handle) CaptureObservations(ctx context.Context, observations []metering.Observation) error {
	if h.capture == nil {
		return fmt.Errorf("%w: shadow V2 handle is not composed", ErrShadowV2CaptureIncomplete)
	}
	return h.capture.CaptureObservations(ctx, observations)
}

// AppendWork persists one immutable economic revision marker.
func (h ShadowV2Handle) AppendWork(ctx context.Context, work billing.EconomicRevisionWork) error {
	if h.capture == nil {
		return fmt.Errorf("%w: shadow V2 handle is not composed", ErrShadowV2CaptureIncomplete)
	}
	return h.capture.AppendWork(ctx, work)
}

// RateAndPersist rates one revision purely and persists the valuation.
func (h ShadowV2Handle) RateAndPersist(ctx context.Context, work billing.EconomicRevisionWork) error {
	if h.capture == nil {
		return fmt.Errorf("%w: shadow V2 handle is not composed", ErrShadowV2CaptureIncomplete)
	}
	return h.capture.RateAndPersist(ctx, work)
}

// AppendReconciliation persists one immutable reconciliation envelope.
func (h ShadowV2Handle) AppendReconciliation(ctx context.Context, work billing.EconomicRevisionWork, reconciliation billing.EconomicReconciliation) error {
	if h.capture == nil {
		return fmt.Errorf("%w: shadow V2 handle is not composed", ErrShadowV2CaptureIncomplete)
	}
	return h.capture.AppendReconciliation(ctx, work, reconciliation)
}
