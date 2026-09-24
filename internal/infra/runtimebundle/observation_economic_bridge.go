package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const (
	observationEconomicRelayBatch    = 32
	observationEconomicRelayInterval = 100 * time.Millisecond
	observationEconomicRelayLease    = 30 * time.Second
	observationEconomicRelayRetry    = 100 * time.Millisecond
	observationEconomicEvidenceLimit = 500
)

var observationEconomicRelaySequence atomic.Uint64

// observationEconomicRelay is the sole owner of the process-local relay
// goroutine. It only transforms durable observations into durable immutable
// work markers; rating workers own pure valuation and all posting seams.
type observationEconomicRelay struct {
	journal  *journalstore.DurableStore
	appender billing.EconomicRevisionWorkAppender
	builder  billing.ObservationEconomicWorkBuilder
	batch    int
	interval time.Duration
	lease    time.Duration
	owner    string

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// configureObservationEconomicBridge wraps the process-owned durable
// observation sink and starts its restartable relay after all billing workers
// have been composed. Memory/disabled/injected non-durable recorders remain a
// no-op because they cannot provide the atomic observation/outbox guarantee.
func configureObservationEconomicBridge(parent context.Context, owner *processResourceOwner, opts *BuildOptions, runtime *meteringRuntime) error {
	if owner == nil || opts == nil || runtime == nil || runtime.Recorder == nil {
		return nil
	}
	journal, ok := runtime.Recorder.(*journalstore.DurableStore)
	if !ok || journal == nil {
		return nil
	}
	appender, ok := opts.Production.BillingStore.(billing.EconomicRevisionWorkAppender)
	if !ok || appender == nil {
		return nil
	}
	builder := opts.Production.BillingObservationEconomicWorkBuilder
	if builder == nil {
		var err error
		builder, err = billing.NewObservationEconomicWorkBuilder(billing.ObservationEconomicWorkBuilderConfig{})
		if err != nil {
			return fmt.Errorf("runtimebundle: observation economic work builder: %w", err)
		}
		opts.Production.BillingObservationEconomicWorkBuilder = builder
	}
	relay := newObservationEconomicRelay(journal, appender, builder)
	runtime.ObservationSink = journalstore.NewObservationSinkWithOutbox(journal)
	owner.Own(func() error { return relay.Stop(context.Background()) })
	if err := relay.Start(parent); err != nil {
		return fmt.Errorf("runtimebundle: start observation economic relay: %w", err)
	}
	return nil
}

func newObservationEconomicRelay(journal *journalstore.DurableStore, appender billing.EconomicRevisionWorkAppender, builder billing.ObservationEconomicWorkBuilder) *observationEconomicRelay {
	return &observationEconomicRelay{
		journal: journal, appender: appender, builder: builder,
		batch: observationEconomicRelayBatch, interval: observationEconomicRelayInterval,
		lease: observationEconomicRelayLease,
		owner: fmt.Sprintf("observation-economic-relay-%d", observationEconomicRelaySequence.Add(1)),
	}
}

func (r *observationEconomicRelay) Start(ctx context.Context) error {
	if r == nil || r.journal == nil || r.appender == nil || r.builder == nil {
		return fmt.Errorf("runtimebundle: incomplete observation economic relay")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done != nil {
		return nil
	}
	workerCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			_ = r.ProcessOnce(workerCtx)
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

func (r *observationEconomicRelay) Stop(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	cancel, done := r.cancel, r.done
	r.mu.Unlock()
	if done == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		// The release uses a fresh context because the worker context is
		// intentionally canceled before durable claims are returned.
		return r.journal.ReleaseObservationOutboxClaims(context.Background(), r.owner)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ProcessOnce drains one bounded claim batch. A failed item is returned to the
// durable queue and does not prevent other independent entries from making
// progress; the first error is retained for diagnostics.
func (r *observationEconomicRelay) ProcessOnce(ctx context.Context) error {
	if r == nil || r.journal == nil || r.appender == nil || r.builder == nil {
		return fmt.Errorf("runtimebundle: incomplete observation economic relay")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	items, err := r.journal.ClaimObservationOutbox(ctx, r.owner, r.batch, r.lease)
	if err != nil {
		return err
	}
	var firstErr error
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			_ = r.journal.RetryObservationOutbox(context.Background(), item.ID, r.owner, 0, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := r.processItem(ctx, item); err != nil {
			if retryErr := r.journal.RetryObservationOutbox(context.Background(), item.ID, r.owner, observationEconomicRelayRetry, err); retryErr != nil {
				err = errors.Join(err, retryErr)
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := r.journal.CompleteObservationOutbox(context.Background(), item.ID, r.owner); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (r *observationEconomicRelay) processItem(ctx context.Context, item journalstore.ObservationOutboxItem) error {
	observation := item.Observation
	blegID, err := economicRelayBLegID(observation)
	if err != nil {
		return err
	}
	evidence := make([]metering.Observation, 0, observationEconomicEvidenceLimit)
	query := journalstore.ObservationQuery{
		StoreID: observation.Subject.StoreID, SubjectKind: metering.SubjectBLeg,
		SubjectID: blegID, Limit: observationEconomicEvidenceLimit,
	}
	for {
		remaining := economics.MaxRatingObservations - len(evidence)
		if remaining <= 0 {
			return fmt.Errorf("runtimebundle: durable economic evidence exceeds %d observations", economics.MaxRatingObservations)
		}
		query.Limit = observationEconomicEvidenceLimit
		if remaining < query.Limit {
			query.Limit = remaining
		}
		page, err := r.journal.ListObservations(ctx, query)
		if err != nil {
			return fmt.Errorf("runtimebundle: load durable economic evidence: %w", err)
		}
		evidence = append(evidence, page.Observations...)
		if page.NextCursor == "" {
			break
		}
		query.Cursor = page.NextCursor
	}
	if err := r.appendLinkedStatementEvidence(ctx, observation, blegID, &evidence); err != nil {
		return err
	}
	if len(evidence) == 0 {
		// The outbox and observation share a transaction; this is a defensive
		// fallback for an older query projection, never a silent acknowledgement.
		evidence = make([]metering.Observation, 0, 1)
	}
	appendRelayObservation(&evidence, observation)
	works, err := r.builder.BuildEconomicRevisionWork(ctx, evidence)
	if err != nil {
		return fmt.Errorf("runtimebundle: build economic revision work: %w", err)
	}
	for _, work := range works {
		// R2 authoritative: production provider monetary work enqueues with
		// the admitted durable owner for its call/work (call-scoped pin
		// preferred over any unbound global marker). Evidence-only customer
		// work uses the generic seam and remains operable.
		if billing.IsMonetaryEconomicShape(work) {
			if poster, ok := r.appender.(billing.EconomicRevisionWorkPostingAppender); ok && poster != nil && !billing.IsNilPort(poster) {
				owner := billing.PostingOwnerV1
				if resolver, ok := r.appender.(billing.EconomicRevisionAdmittedOwnerResolver); ok && resolver != nil && !billing.IsNilPort(resolver) {
					if resolved, rerr := resolver.ResolveEconomicRevisionPostingOwner(ctx, work); rerr != nil {
						return fmt.Errorf("runtimebundle: resolve economic posting owner: %w", rerr)
					} else if resolved == billing.PostingOwnerV1 || resolved == billing.PostingOwnerV2 {
						owner = resolved
					}
				}
				if err := poster.AppendProviderPostingEconomicRevisionWork(ctx, work, owner); err != nil {
					return fmt.Errorf("runtimebundle: append economic revision work: %w", err)
				}
				continue
			}
		}
		if err := r.appender.AppendEconomicRevisionWork(ctx, work); err != nil {
			return fmt.Errorf("runtimebundle: append economic revision work: %w", err)
		}
	}
	return nil
}

func (r *observationEconomicRelay) appendLinkedStatementEvidence(ctx context.Context, source metering.Observation, blegID string, evidence *[]metering.Observation) error {
	if r == nil || r.journal == nil || evidence == nil {
		return fmt.Errorf("runtimebundle: incomplete statement evidence lookup")
	}
	providerAccountKey := strings.TrimSpace(source.Subject.ProviderAccountKey)
	if providerAccountKey == "" {
		providerAccountKey = strings.TrimSpace(source.Correlation.ProviderAccountKey)
	}
	streamID := strings.TrimSpace(source.StreamID)
	if providerAccountKey == "" && streamID == "" {
		return nil
	}
	queries := make([]journalstore.ObservationQuery, 0, 2)
	if providerAccountKey != "" {
		queries = append(queries, journalstore.ObservationQuery{StoreID: source.Subject.StoreID, ProviderAccountKey: providerAccountKey, Limit: observationEconomicEvidenceLimit})
	}
	if streamID != "" && streamID != source.Subject.BLegID {
		queries = append(queries, journalstore.ObservationQuery{StoreID: source.Subject.StoreID, StreamID: streamID, Limit: observationEconomicEvidenceLimit})
	}
	for _, query := range queries {
		for {
			remaining := economics.MaxRatingObservations - len(*evidence)
			if remaining <= 0 {
				return fmt.Errorf("runtimebundle: durable economic evidence exceeds %d observations", economics.MaxRatingObservations)
			}
			if query.Limit > remaining {
				query.Limit = remaining
			}
			page, err := r.journal.ListObservations(ctx, query)
			if err != nil {
				return fmt.Errorf("runtimebundle: load linked statement evidence: %w", err)
			}
			for _, candidate := range page.Observations {
				if !linkedStatementObservation(candidate, source, blegID) {
					continue
				}
				appendRelayObservation(evidence, candidate)
			}
			if page.NextCursor == "" {
				break
			}
			query.Cursor = page.NextCursor
		}
	}
	return nil
}

func linkedStatementObservation(candidate, source metering.Observation, blegID string) bool {
	if !verifiedStatementObservationForRelay(candidate) || candidate.Correlation.BLegID != blegID {
		return false
	}
	if candidate.Subject.StoreID != source.Subject.StoreID || candidate.Correlation.StoreID != source.Subject.StoreID {
		return false
	}
	if candidate.Subject.AccountID != "" && source.Subject.AccountID != "" && candidate.Subject.AccountID != source.Subject.AccountID {
		return false
	}
	if candidate.Correlation.ALegID != "" && source.Correlation.ALegID != "" && candidate.Correlation.ALegID != source.Correlation.ALegID {
		return false
	}
	for _, pair := range [][2]string{
		{firstRelayNonEmpty(candidate.Subject.BillingCallID, candidate.Correlation.BillingCallID), firstRelayNonEmpty(source.Subject.BillingCallID, source.Correlation.BillingCallID)},
		{firstRelayNonEmpty(candidate.Subject.CallID, candidate.Correlation.CallID), firstRelayNonEmpty(source.Subject.CallID, source.Correlation.CallID)},
		{firstRelayNonEmpty(candidate.Subject.ProviderAccountKey, candidate.Correlation.ProviderAccountKey), firstRelayNonEmpty(source.Subject.ProviderAccountKey, source.Correlation.ProviderAccountKey)},
		{firstRelayNonEmpty(candidate.Subject.ProviderRequestID, candidate.Correlation.ProviderRequestID), firstRelayNonEmpty(source.Subject.ProviderRequestID, source.Correlation.ProviderRequestID)},
		{firstRelayNonEmpty(candidate.Subject.ProviderChargeID, candidate.Correlation.ProviderChargeID), firstRelayNonEmpty(source.Subject.ProviderChargeID, source.Correlation.ProviderChargeID)},
	} {
		if pair[0] != "" && pair[1] != "" && pair[0] != pair[1] {
			return false
		}
	}
	return true
}

func firstRelayNonEmpty(left, right string) string {
	if strings.TrimSpace(left) != "" {
		return strings.TrimSpace(left)
	}
	return strings.TrimSpace(right)
}

func appendRelayObservation(evidence *[]metering.Observation, observation metering.Observation) {
	if evidence == nil || observationInEconomicRelayEvidence(*evidence, observation) {
		return
	}
	*evidence = append(*evidence, observation)
}

func economicRelayBLegID(observation metering.Observation) (string, error) {
	switch observation.Subject.Kind {
	case metering.SubjectBLeg:
		if observation.Subject.BLegID == "" {
			return "", fmt.Errorf("runtimebundle: B-leg economic observation requires B-leg ID")
		}
		return observation.Subject.BLegID, nil
	case metering.SubjectStatementLine:
		if !verifiedStatementObservationForRelay(observation) {
			return "", fmt.Errorf("runtimebundle: statement economic observation requires verified importer provenance")
		}
		if observation.Correlation.BLegID == "" {
			return "", fmt.Errorf("runtimebundle: statement economic observation requires one correlated B-leg")
		}
		return observation.Correlation.BLegID, nil
	default:
		return "", fmt.Errorf("runtimebundle: unsupported economic subject %q", observation.Subject.Kind)
	}
}

func verifiedStatementObservationForRelay(observation metering.Observation) bool {
	return observation.Origin == metering.OriginStatement &&
		observation.Acquisition == metering.AcquisitionStatementImporter &&
		observation.Authority == metering.AuthorityVerifiedStatement &&
		observation.Subject.Kind == metering.SubjectStatementLine &&
		observation.Subject.StatementLineID != ""
}

func observationInEconomicRelayEvidence(evidence []metering.Observation, wanted metering.Observation) bool {
	for _, observation := range evidence {
		if observation.IdentityKey() == wanted.IdentityKey() {
			return true
		}
	}
	return false
}
