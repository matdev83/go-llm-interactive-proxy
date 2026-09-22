package billingstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Shadow V2 capture for Task 17.2 (Migration Strategy step 4).
//
// Shadow mode persists V2 observations, valuations, and reconciliation while
// V1 remains the sole monetary writer. This runner exposes only immutable
// capture/rating/reconciliation ports: observation evidence, economic work
// markers, pure valuation results, and reconciliation envelopes. It holds no
// balance, unit-ledger, provider-payable, journal, adjustment, or
// exposure-settlement port and performs no stream-time money write. Shadow
// observations live in V2 observation storage under existing execution
// lineage; they never become usage call/leg rows or claim state.
//
// All methods are synchronous, bounded, and context-aware. Restart/replay of
// the same stable IDs is idempotent through the underlying durable identity
// fences; canceled contexts fail closed before any durable write.

var (
	// ErrShadowV2Incomplete identifies missing shadow configuration or ports.
	ErrShadowV2Incomplete = errors.New("billingstore: shadow V2 capture is incomplete")
	// ErrShadowV2Scope identifies a store-scope mismatch.
	ErrShadowV2Scope = errors.New("billingstore: shadow V2 capture store scope mismatch")
)

const (
	shadowV2MaxObservationsLower = 1
	shadowV2MaxObservationsUpper = 1024
)

// ShadowV2CaptureConfig is the explicit typed shadow-mode configuration. It
// carries no monetary account, tariff, balance, or posting field; StoreID
// scopes every durable write and MaxObservations bounds one work envelope.
type ShadowV2CaptureConfig struct {
	StoreID         string
	MaxObservations int
}

// Validate fails closed on missing scope or unbounded work size.
func (c ShadowV2CaptureConfig) Validate() error {
	if strings.TrimSpace(c.StoreID) == "" {
		return fmt.Errorf("%w: store scope is required", ErrShadowV2Incomplete)
	}
	if c.MaxObservations < shadowV2MaxObservationsLower || c.MaxObservations > shadowV2MaxObservationsUpper {
		return fmt.Errorf("%w: max observations %d must be within [%d,%d]",
			ErrShadowV2Incomplete, c.MaxObservations, shadowV2MaxObservationsLower, shadowV2MaxObservationsUpper)
	}
	return nil
}

// ShadowV2Capture is the explicit no-post shadow orchestrator. Each field is a
// narrow pure port; no monetary writer type appears in this struct by design.
// Evidence capture uses the capture-only ShadowEvidenceSink, never the
// ordinary terminal handoff, so shadow legs/calls cannot become V1 posting or
// claim work.
type ShadowV2Capture struct {
	storeID         string
	maxObservations int
	evidence        billing.ShadowEvidenceSink
	work            billing.EconomicRevisionWorkAppender
	results         billing.EconomicRevisionResultStore
	reconciliations billing.EconomicRevisionReconciliationStore
	rater           billing.PostUsageRater
}

// NewShadowV2Capture constructs the shadow runner with explicit pure ports.
// A nil or typed-nil port fails closed. The V1 monetary writer is intentionally
// not a parameter: V1 ownership stays on its existing settlement path while
// this handle cannot address it.
func NewShadowV2Capture(
	cfg ShadowV2CaptureConfig,
	evidence billing.ShadowEvidenceSink,
	work billing.EconomicRevisionWorkAppender,
	results billing.EconomicRevisionResultStore,
	reconciliations billing.EconomicRevisionReconciliationStore,
	rater billing.PostUsageRater,
) (*ShadowV2Capture, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if billing.IsNilPort(evidence) || billing.IsNilPort(work) || billing.IsNilPort(results) || billing.IsNilPort(reconciliations) || billing.IsNilPort(rater) {
		return nil, fmt.Errorf("%w: evidence, work, result, reconciliation, and rater ports are required", ErrShadowV2Incomplete)
	}
	return &ShadowV2Capture{
		storeID:         strings.TrimSpace(cfg.StoreID),
		maxObservations: cfg.MaxObservations,
		evidence:        evidence,
		work:            work,
		results:         results,
		reconciliations: reconciliations,
		rater:           rater,
	}, nil
}

func (s *ShadowV2Capture) checkContext(ctx context.Context) error {
	if s == nil || billing.IsNilPort(s.evidence) || billing.IsNilPort(s.work) || billing.IsNilPort(s.results) || billing.IsNilPort(s.reconciliations) || billing.IsNilPort(s.rater) {
		return fmt.Errorf("%w: shadow V2 capture is not constructed", ErrShadowV2Incomplete)
	}
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrShadowV2Incomplete)
	}
	return ctx.Err()
}

// shadowEffectiveWorkObservations counts embedded observations plus explicit
// references without normalizing. Both forms consume the configured bound.
func shadowEffectiveWorkObservations(work billing.EconomicRevisionWork) int {
	return len(work.Input.Observations) + len(work.Input.ObservationRefs)
}

func (s *ShadowV2Capture) checkWorkBounds(work billing.EconomicRevisionWork) error {
	if n := shadowEffectiveWorkObservations(work); n > s.maxObservations {
		return fmt.Errorf("%w: work observations %d exceed shadow bound %d",
			ErrShadowV2Incomplete, n, s.maxObservations)
	}
	return nil
}

// CaptureObservations durably captures immutable V2 source-separated
// observations through the capture-only evidence port. Every observation
// counts against the configured bound, and every observation's store scope
// must match the shadow scope. Observations reference existing executions by
// lineage; capturing them creates no usage call/leg rows, claim state,
// provider-cost work, or payable intent. There is deliberately no shadow
// call-closure method: closure has no independent V2 evidence beyond the
// observations, valuations, and reconciliation already persisted here.
func (s *ShadowV2Capture) CaptureObservations(ctx context.Context, observations []metering.Observation) error {
	if err := s.checkContext(ctx); err != nil {
		return err
	}
	if len(observations) > s.maxObservations {
		return fmt.Errorf("%w: captured observations %d exceed shadow bound %d", ErrShadowV2Incomplete, len(observations), s.maxObservations)
	}
	for i := range observations {
		if strings.TrimSpace(observations[i].Subject.StoreID) != s.storeID {
			return fmt.Errorf("%w: captured observation %d store %q does not match shadow scope %q",
				ErrShadowV2Scope, i, observations[i].Subject.StoreID, s.storeID)
		}
		if strings.TrimSpace(observations[i].Correlation.StoreID) != s.storeID {
			return fmt.Errorf("%w: captured observation %d correlation store %q does not match shadow scope %q",
				ErrShadowV2Scope, i, observations[i].Correlation.StoreID, s.storeID)
		}
	}
	return s.evidence.AppendObservations(ctx, observations)
}

// AppendWork persists one immutable economic revision marker idempotently.
// The work subject must belong to this shadow store scope. The effective
// observation bound applies to embedded, ref-only, and combined forms before
// normalization.
func (s *ShadowV2Capture) AppendWork(ctx context.Context, work billing.EconomicRevisionWork) error {
	if err := s.checkContext(ctx); err != nil {
		return err
	}
	if err := s.checkWorkBounds(work); err != nil {
		return err
	}
	normalized, err := work.Normalize()
	if err != nil {
		return err
	}
	if strings.TrimSpace(normalized.Subject.StoreID) != s.storeID {
		return fmt.Errorf("%w: work store %q does not match shadow scope %q",
			ErrShadowV2Scope, normalized.Subject.StoreID, s.storeID)
	}
	return s.work.AppendEconomicRevisionWork(ctx, normalized)
}

// RateAndPersist computes the pure valuation for one revision through the
// single shared worker normalization contract and persists it idempotently.
// The contract preserves the full valid rater clone and anchors CreatedAt to
// immutable work metadata, so repeated or overlapping calls with a
// deterministic rater persist one stable row. The durable store provides
// idempotency; no probe-gated no-rerate promise is made here. No balance,
// journal, unit, or provider-payable mutation occurs on this path.
func (s *ShadowV2Capture) RateAndPersist(ctx context.Context, work billing.EconomicRevisionWork) error {
	if err := s.checkContext(ctx); err != nil {
		return err
	}
	if err := s.checkWorkBounds(work); err != nil {
		return err
	}
	normalized, err := work.Normalize()
	if err != nil {
		return err
	}
	if strings.TrimSpace(normalized.Subject.StoreID) != s.storeID {
		return fmt.Errorf("%w: work store %q does not match shadow scope %q",
			ErrShadowV2Scope, normalized.Subject.StoreID, s.storeID)
	}
	identity, err := normalized.Identity()
	if err != nil {
		return err
	}
	draft, err := s.rater.Rate(ctx, normalized.Input.Clone())
	if err != nil {
		return err
	}
	valuation, err := billing.NormalizeRevisionValuationForWork(normalized, identity, draft)
	if err != nil {
		return err
	}
	return s.results.AppendEconomicRevisionResult(ctx, normalized, billing.EconomicRevisionResult{Valuation: valuation})
}

// AppendReconciliation persists one immutable reconciliation envelope for
// dependency-anchored work. The envelope identity must match the work; no
// account, journal, exposure, or financial head row is touched. The effective
// observation bound applies before normalization.
func (s *ShadowV2Capture) AppendReconciliation(ctx context.Context, work billing.EconomicRevisionWork, reconciliation billing.EconomicReconciliation) error {
	if err := s.checkContext(ctx); err != nil {
		return err
	}
	if err := s.checkWorkBounds(work); err != nil {
		return err
	}
	normalized, err := work.Normalize()
	if err != nil {
		return err
	}
	if strings.TrimSpace(normalized.Subject.StoreID) != s.storeID {
		return fmt.Errorf("%w: work store %q does not match shadow scope %q",
			ErrShadowV2Scope, normalized.Subject.StoreID, s.storeID)
	}
	return s.reconciliations.AppendEconomicRevisionReconciliation(ctx, normalized, reconciliation)
}
