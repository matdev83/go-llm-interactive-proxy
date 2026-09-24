package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	// observationEconomicCandidateBudgetRetryFloor is the minimum durable delay
	// before an over-budget linked-statement item is rescanned. The candidate
	// budget sentinel is permanent under immutable append-only evidence, so the
	// generic 100ms transient retry would rescan the same B-leg on every relay
	// tick without any chance of progress.
	observationEconomicCandidateBudgetRetryFloor = time.Minute
	// observationEconomicCandidateBudgetRetryCap bounds the exponential growth
	// so a permanently over-budget item is retried rarely rather than never.
	observationEconomicCandidateBudgetRetryCap = time.Hour
	// observationEconomicCandidateBudgetDeferralEvent is the bounded structured
	// diagnostic emitted at the relay boundary when the permanent failure is
	// durably deferred. It names no raw evidence, payload, or provider content.
	observationEconomicCandidateBudgetDeferralEvent = "lip.observation_economic_relay_candidate_budget_deferred"
	// observationEconomicCandidateBudgetPersistFailureEvent is the bounded
	// structured diagnostic emitted when the retry update that would persist a
	// candidate-budget deferral fails. It deliberately carries no raw error text
	// and no evidence or payload content.
	observationEconomicCandidateBudgetPersistFailureEvent = "lip.observation_economic_relay_candidate_budget_deferral_persist_failed"
	// observationEconomicStatementCandidateBudget is the hard bound on raw
	// verified statement-line rows a single B-leg relay item may enumerate. It
	// is deliberately a small multiple of the accepted-evidence cap so normal
	// volumes pass and pathological candidate growth fails closed instead of
	// paging without bound.
	observationEconomicStatementCandidateBudget = 4 * economics.MaxRatingObservations
)

// errObservationEconomicStatementCandidateBudget is a retryable, fail-closed
// refusal: the durable outbox item stays pending and no truncated work marker
// is ever emitted for the prefix that was read.
var errObservationEconomicStatementCandidateBudget = errors.New("runtimebundle: linked statement candidate budget exceeded")

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
	// statementCandidateBudget is a test seam for the package hard budget.
	// Zero uses observationEconomicStatementCandidateBudget.
	statementCandidateBudget int
	// log is the optional process logger seam. Nil disables diagnostics.
	log *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// configureObservationEconomicBridge wraps the process-owned durable
// observation sink and starts its restartable relay after all billing workers
// have been composed. Memory/disabled/injected non-durable recorders remain a
// no-op because they cannot provide the atomic observation/outbox guarantee.
func configureObservationEconomicBridge(parent context.Context, owner *processResourceOwner, opts *BuildOptions, runtime *meteringRuntime, log *slog.Logger) error {
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
	relay.log = log
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
		lease:                    observationEconomicRelayLease,
		owner:                    fmt.Sprintf("observation-economic-relay-%d", observationEconomicRelaySequence.Add(1)),
		statementCandidateBudget: observationEconomicStatementCandidateBudget,
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
			isCandidateBudget := errors.Is(err, errObservationEconomicStatementCandidateBudget)
			retryDelay := observationEconomicRelayRetry
			if isCandidateBudget {
				// The candidate budget sentinel is permanent for immutable
				// append-only evidence, so back the retry off from the
				// persisted attempt count.
				retryDelay = observationEconomicCandidateBudgetRetryDelay(item.AttemptCount)
			}
			retryErr := r.journal.RetryObservationOutbox(context.Background(), item.ID, r.owner, retryDelay, err)
			if retryErr != nil {
				// The deferral was never persisted (failed update or lost
				// claim), so it must never be logged as durable. Emit only a
				// bounded safe diagnostic because Start discards the error.
				if isCandidateBudget {
					r.logCandidateBudgetDeferralPersistFailure(ctx, item)
				}
				err = errors.Join(err, retryErr)
			} else if isCandidateBudget {
				// Only a successful durable update may be reported as a
				// deferral, so the warning reflects persisted outcome.
				r.logCandidateBudgetDeferral(ctx, item, retryDelay)
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

// observationEconomicCandidateBudgetRetryDelay derives a bounded exponential
// backoff from the durable attempt count. The first deferral waits the floor
// and each later attempt doubles it until the cap. It is overflow-safe because
// it stops at the cap well before any shift could overflow, and it is bounded
// because the loop returns as soon as the cap is reached.
func observationEconomicCandidateBudgetRetryDelay(attemptCount int) time.Duration {
	delay := observationEconomicCandidateBudgetRetryFloor
	if attemptCount <= 1 {
		return delay
	}
	for attempt := 1; attempt < attemptCount; attempt++ {
		if delay >= observationEconomicCandidateBudgetRetryCap {
			return observationEconomicCandidateBudgetRetryCap
		}
		delay *= 2
	}
	if delay > observationEconomicCandidateBudgetRetryCap {
		return observationEconomicCandidateBudgetRetryCap
	}
	return delay
}

// logCandidateBudgetDeferral emits one bounded structured diagnostic when an
// over-budget item is durably deferred. It must only be called after the retry
// update has committed, so the record reflects a durable outcome. Because the
// deferral is backed off, the record is emitted at most once per computed delay
// per item rather than on every relay tick, and it carries only relay identity
// and timing.
func (r *observationEconomicRelay) logCandidateBudgetDeferral(ctx context.Context, item journalstore.ObservationOutboxItem, delay time.Duration) {
	if r == nil || r.log == nil {
		return
	}
	r.log.LogAttrs(
		ctx, slog.LevelWarn, observationEconomicCandidateBudgetDeferralEvent,
		slog.Int64("outbox_id", item.ID),
		slog.String("observation_id", item.Observation.ID),
		slog.String("subject_kind", string(item.Observation.Subject.Kind)),
		slog.Int("attempt_count", item.AttemptCount),
		slog.Duration("retry_delay", delay),
	)
}

// logCandidateBudgetDeferralPersistFailure emits a bounded structured
// diagnostic when the durable retry update that would defer an over-budget item
// failed (for example a lost claim or a database write error). It deliberately
// omits the raw error and any evidence or payload content; the caller still
// joins the underlying error for the relay loop's normal handling.
func (r *observationEconomicRelay) logCandidateBudgetDeferralPersistFailure(ctx context.Context, item journalstore.ObservationOutboxItem) {
	if r == nil || r.log == nil {
		return
	}
	r.log.LogAttrs(
		ctx, slog.LevelWarn, observationEconomicCandidateBudgetPersistFailureEvent,
		slog.Int64("outbox_id", item.ID),
		slog.String("observation_id", item.Observation.ID),
		slog.String("subject_kind", string(item.Observation.Subject.Kind)),
		slog.Int("attempt_count", item.AttemptCount),
	)
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
		query.Limit = min(remaining, observationEconomicEvidenceLimit)
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

// appendLinkedStatementEvidence resolves verified statement/correction
// observations for the source B-leg through a selective indexed correlation
// bound. Candidates are restricted at the database to verified statement-line
// rows for the trusted store + B-leg, so a B-leg's unrelated usage history and
// unverified statement rows are never paged. Candidate work is additionally
// hard-bounded: once the documented budget is exhausted while more candidates
// remain, the lookup fails closed with a retryable error instead of
// acknowledging a truncated prefix. linkedStatementObservation still enforces
// store, account, A-leg and call/request lineage isolation and remains the
// authoritative validator for every accepted candidate.
func (r *observationEconomicRelay) appendLinkedStatementEvidence(ctx context.Context, source metering.Observation, blegID string, evidence *[]metering.Observation) error {
	if r == nil || r.journal == nil || evidence == nil {
		return fmt.Errorf("runtimebundle: incomplete statement evidence lookup")
	}
	blegID = strings.TrimSpace(blegID)
	if blegID == "" {
		return nil
	}
	budget := r.statementCandidateBudget
	if budget <= 0 {
		budget = observationEconomicStatementCandidateBudget
	}
	query := journalstore.ObservationQuery{
		StoreID: source.Subject.StoreID, CorrelationBLegID: blegID,
		StatementEvidenceOnly: true, Limit: observationEconomicEvidenceLimit,
	}
	examined := 0
	for {
		// The candidate budget is enforced before each page so total examined
		// rows can never exceed it; a page is only fetched while work remains.
		left := budget - examined
		if left <= 0 {
			return fmt.Errorf("%w: examined %d verified statement candidates for B-leg %q (budget %d)",
				errObservationEconomicStatementCandidateBudget, examined, blegID, budget)
		}
		if left < observationEconomicEvidenceLimit {
			query.Limit = left
		} else {
			query.Limit = observationEconomicEvidenceLimit
		}
		page, err := r.journal.ListObservations(ctx, query)
		if err != nil {
			return fmt.Errorf("runtimebundle: load linked statement evidence: %w", err)
		}
		examined += len(page.Observations)
		for _, candidate := range page.Observations {
			if !linkedStatementObservation(candidate, source, blegID) {
				continue
			}
			if len(*evidence) >= economics.MaxRatingObservations {
				return fmt.Errorf("runtimebundle: durable economic evidence exceeds %d observations", economics.MaxRatingObservations)
			}
			appendRelayObservation(evidence, candidate)
		}
		if page.NextCursor == "" {
			break
		}
		query.Cursor = page.NextCursor
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
