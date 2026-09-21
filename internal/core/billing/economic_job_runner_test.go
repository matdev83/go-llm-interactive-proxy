package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// economicJobRunnerTestStore is an in-memory implementation of every port the
// application runner consumes. It models the durable queue, result and
// reconciliation semantics closely enough to exercise orchestration without a
// database.
type economicJobRunnerTestState struct {
	status      EconomicWorkStatus
	owner       string
	fence       uint64
	nextAttempt time.Time
	retryReason EconomicWorkReason
	attempts    int
}

type economicJobRunnerTestStore struct {
	mu                 sync.Mutex
	order              []string
	works              map[string]EconomicRevisionWork
	states             map[string]*economicJobRunnerTestState
	results            map[string]EconomicRevisionResult
	valuations         map[string]economics.Valuation
	reconciliations    map[string]EconomicReconciliation
	completeFailures   map[string]int
	completeClaimLost  map[string]int
	expired            map[string]bool
	skipDependencyGate bool
}

func newEconomicJobRunnerTestStore() *economicJobRunnerTestStore {
	return &economicJobRunnerTestStore{
		works: make(map[string]EconomicRevisionWork), states: make(map[string]*economicJobRunnerTestState),
		results: make(map[string]EconomicRevisionResult), valuations: make(map[string]economics.Valuation),
		reconciliations:  make(map[string]EconomicReconciliation),
		completeFailures: make(map[string]int), completeClaimLost: make(map[string]int), expired: make(map[string]bool),
	}
}

func (s *economicJobRunnerTestStore) appendWork(t *testing.T, work EconomicRevisionWork) EconomicRevisionIdentity {
	t.Helper()
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.works[identity.Key()]; !exists {
		s.order = append(s.order, identity.Key())
	}
	s.works[identity.Key()] = normalized
	if s.states[identity.Key()] == nil {
		s.states[identity.Key()] = &economicJobRunnerTestState{status: EconomicWorkStatusPending}
	}
	return identity
}

func (s *economicJobRunnerTestStore) state(key string) *economicJobRunnerTestState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[key]
}

func (s *economicJobRunnerTestStore) status(key string) EconomicWorkStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states[key] == nil {
		return ""
	}
	return s.states[key].status
}

func (s *economicJobRunnerTestStore) retryReason(key string) EconomicWorkReason {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states[key] == nil {
		return ""
	}
	return s.states[key].retryReason
}

func (s *economicJobRunnerTestStore) nextAttempt(key string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states[key] == nil {
		return time.Time{}
	}
	return s.states[key].nextAttempt
}

func (s *economicJobRunnerTestStore) resultCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.results)
}

func (s *economicJobRunnerTestStore) reconciliationCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reconciliations)
}

func (s *economicJobRunnerTestStore) expireLeases() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, state := range s.states {
		if state.status == EconomicWorkStatusProcessing {
			s.expired[key] = true
		}
	}
}

func (s *economicJobRunnerTestStore) dependenciesSatisfiedLocked(work EconomicRevisionWork) bool {
	for _, dependency := range work.Dependencies {
		identity, err := dependency.OutputIdentity()
		if err != nil {
			return false
		}
		if _, ok := s.valuations[identity.ValuationKey()]; !ok {
			return false
		}
	}
	return true
}

func (s *economicJobRunnerTestStore) ClaimEconomicRevisionWorkBatch(_ context.Context, queue EconomicQueue, owner string, lease time.Duration, limit int) ([]EconomicRevisionClaimedWork, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	claims := make([]EconomicRevisionClaimedWork, 0, limit)
	for _, key := range s.order {
		if len(claims) >= limit {
			break
		}
		work := s.works[key]
		if work.Queue != queue {
			continue
		}
		state := s.states[key]
		switch state.status {
		case EconomicWorkStatusCompleted, EconomicWorkStatusFailed:
			continue
		case EconomicWorkStatusProcessing:
			if !s.expired[key] {
				continue
			}
		}
		if !state.nextAttempt.IsZero() && state.nextAttempt.After(now) {
			continue
		}
		if !s.skipDependencyGate && !s.dependenciesSatisfiedLocked(work) {
			continue
		}
		state.status = EconomicWorkStatusProcessing
		state.owner = owner
		state.fence++
		state.attempts++
		delete(s.expired, key)
		claims = append(claims, EconomicRevisionClaimedWork{
			Work:  work,
			Claim: EconomicRevisionWorkClaim{Owner: owner, Fence: state.fence, LeaseUntil: now.Add(lease)},
		})
	}
	return claims, nil
}

func (s *economicJobRunnerTestStore) CompleteEconomicRevisionWork(_ context.Context, work EconomicRevisionWork, claim EconomicRevisionWorkClaim) error {
	identity, err := work.Identity()
	if err != nil {
		return err
	}
	key := identity.Key()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completeClaimLost[key] > 0 {
		s.completeClaimLost[key]--
		return fmt.Errorf("%w: injected supersession for %q", ErrEconomicRevisionClaimLost, key)
	}
	if s.completeFailures[key] > 0 {
		s.completeFailures[key]--
		return fmt.Errorf("injected complete failure for %q", key)
	}
	state := s.states[key]
	if state == nil || state.status != EconomicWorkStatusProcessing || state.owner != claim.Owner || state.fence != claim.Fence {
		return fmt.Errorf("%w: complete %q", ErrEconomicRevisionClaimLost, key)
	}
	state.status = EconomicWorkStatusCompleted
	state.owner = ""
	state.retryReason = ""
	return nil
}

func (s *economicJobRunnerTestStore) RetryEconomicRevisionWorkWithReason(_ context.Context, work EconomicRevisionWork, claim EconomicRevisionWorkClaim, reason EconomicWorkReason, nextAttemptAt time.Time) error {
	identity, err := work.Identity()
	if err != nil {
		return err
	}
	key := identity.Key()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[key]
	if state == nil || state.status != EconomicWorkStatusProcessing || state.owner != claim.Owner || state.fence != claim.Fence {
		return fmt.Errorf("%w: retry %q", ErrEconomicRevisionClaimLost, key)
	}
	state.status = EconomicWorkStatusPending
	state.owner = ""
	state.retryReason = reason
	state.nextAttempt = nextAttemptAt.UTC()
	return nil
}

func (s *economicJobRunnerTestStore) FailEconomicRevisionWork(_ context.Context, work EconomicRevisionWork, claim EconomicRevisionWorkClaim, reason EconomicWorkReason) error {
	identity, err := work.Identity()
	if err != nil {
		return err
	}
	key := identity.Key()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[key]
	if state == nil || state.status != EconomicWorkStatusProcessing || state.owner != claim.Owner || state.fence != claim.Fence {
		return fmt.Errorf("%w: fail %q", ErrEconomicRevisionClaimLost, key)
	}
	state.status = EconomicWorkStatusFailed
	state.owner = ""
	state.retryReason = reason
	return nil
}

func (s *economicJobRunnerTestStore) EconomicRevisionQueueBacklog(_ context.Context, queue EconomicQueue) (EconomicRevisionBacklog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	backlog := EconomicRevisionBacklog{Queue: queue}
	var oldest time.Time
	var next time.Time
	for _, key := range s.order {
		work := s.works[key]
		if work.Queue != queue {
			continue
		}
		state := s.states[key]
		switch state.status {
		case EconomicWorkStatusPending:
			backlog.Pending++
			if oldest.IsZero() || work.CreatedAt.Before(oldest) {
				oldest = work.CreatedAt
			}
			if !state.nextAttempt.IsZero() && (next.IsZero() || state.nextAttempt.Before(next)) {
				next = state.nextAttempt
			}
			if !s.dependenciesSatisfiedLocked(work) {
				backlog.IncompleteDependencies++
			}
		case EconomicWorkStatusProcessing:
			backlog.Processing++
			if oldest.IsZero() || work.CreatedAt.Before(oldest) {
				oldest = work.CreatedAt
			}
		case EconomicWorkStatusCompleted:
			backlog.Completed++
		case EconomicWorkStatusFailed:
			backlog.Failed++
		}
	}
	if !oldest.IsZero() {
		backlog.OldestPendingAt = oldest
		if age := time.Since(oldest); age > 0 {
			backlog.OldestPendingAge = age
		}
	}
	backlog.NextAttemptAt = next
	return backlog, nil
}

func (s *economicJobRunnerTestStore) EconomicRevisionDependencyChecks(_ context.Context, work EconomicRevisionWork) ([]EconomicJobDependencyCheck, error) {
	normalized, err := work.Normalize()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	checks := make([]EconomicJobDependencyCheck, 0, len(normalized.Dependencies))
	for _, dependency := range normalized.Dependencies {
		status := EconomicJobDependencyMissing
		if identity, err := dependency.OutputIdentity(); err == nil {
			if _, ok := s.valuations[identity.ValuationKey()]; ok {
				status = EconomicJobDependencySatisfied
			}
		}
		checks = append(checks, EconomicJobDependencyCheck{Dependency: dependency, Status: status})
	}
	return checks, nil
}

func (s *economicJobRunnerTestStore) HasEconomicRevisionResult(_ context.Context, identity EconomicRevisionIdentity) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.results[identity.Key()]
	return ok, nil
}

func (s *economicJobRunnerTestStore) AppendEconomicRevisionResult(_ context.Context, work EconomicRevisionWork, result EconomicRevisionResult) error {
	normalized, err := work.Normalize()
	if err != nil {
		return err
	}
	identity, err := normalized.Identity()
	if err != nil {
		return err
	}
	if result.Valuation.ID != identity.ValuationKey() {
		return fmt.Errorf("%w: valuation id", ErrEconomicRevisionInputMismatch)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.results[identity.Key()]; ok {
		if existing.Valuation.Fingerprint() != result.Valuation.Fingerprint() {
			return fmt.Errorf("%w: result %q", ErrEconomicRevisionConflict, identity.Key())
		}
		return nil
	}
	s.results[identity.Key()] = result
	s.valuations[result.Valuation.ID] = result.Valuation
	return nil
}

func (s *economicJobRunnerTestStore) LoadEconomicRevisionDependencyOutput(_ context.Context, dependency EconomicJobDependency) (economics.Valuation, error) {
	identity, err := dependency.OutputIdentity()
	if err != nil {
		return economics.Valuation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	valuation, ok := s.valuations[identity.ValuationKey()]
	if !ok {
		return economics.Valuation{}, fmt.Errorf("%w: %s", ErrEconomicRevisionDependencyOutputMissing, identity.Key())
	}
	return valuation, nil
}

func (s *economicJobRunnerTestStore) HasEconomicRevisionReconciliation(_ context.Context, identity EconomicRevisionIdentity) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.reconciliations[identity.Key()]
	return ok, nil
}

func (s *economicJobRunnerTestStore) AppendEconomicRevisionReconciliation(_ context.Context, work EconomicRevisionWork, reconciliation EconomicReconciliation) error {
	normalized, err := work.Normalize()
	if err != nil {
		return err
	}
	identity, err := normalized.Identity()
	if err != nil {
		return err
	}
	normalizedReconciliation, err := reconciliation.Normalize()
	if err != nil {
		return err
	}
	if normalizedReconciliation.ID != identity.ReconciliationKey() {
		return fmt.Errorf("%w: reconciliation id", ErrEconomicRevisionInputMismatch)
	}
	if normalizedReconciliation.Version != identity.EvidenceRevision {
		return fmt.Errorf("%w: reconciliation version", ErrEconomicRevisionInputMismatch)
	}
	if !sameSubject(normalizedReconciliation.Subject, normalized.Subject) {
		return fmt.Errorf("%w: reconciliation subject", ErrEconomicRevisionSubjectMismatch)
	}
	if normalizedReconciliation.Basis != normalized.Input.Basis {
		return fmt.Errorf("%w: reconciliation basis", ErrEconomicRevisionBasisMismatch)
	}
	if normalizedReconciliation.InputSetHash != normalized.InputSetHash {
		return fmt.Errorf("%w: reconciliation input hash", ErrEconomicRevisionInputMismatch)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.reconciliations[identity.Key()]; ok {
		existingPayload, existingErr := json.Marshal(existing)
		incomingPayload, incomingErr := json.Marshal(normalizedReconciliation)
		if existingErr != nil || incomingErr != nil || string(existingPayload) != string(incomingPayload) {
			return fmt.Errorf("%w: reconciliation %q", ErrEconomicRevisionConflict, identity.Key())
		}
		return nil
	}
	s.reconciliations[identity.Key()] = normalizedReconciliation
	return nil
}

type economicJobRunnerTestRater struct {
	mu    sync.Mutex
	calls []economics.PostUsageRatingInput
	errs  []error
	hook  func()
}

func (r *economicJobRunnerTestRater) Rate(ctx context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	r.mu.Lock()
	call := len(r.calls)
	r.calls = append(r.calls, input.Clone())
	errs := append([]error(nil), r.errs...)
	hook := r.hook
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	if err := ctx.Err(); err != nil {
		return economics.Valuation{}, err
	}
	if call < len(errs) && errs[call] != nil {
		return economics.Valuation{}, errs[call]
	}
	refs := append([]metering.ObservationRef(nil), input.ObservationRefs...)
	if len(refs) == 0 {
		for _, observation := range input.Observations {
			ref, err := observation.Ref(input.Subject.StoreID)
			if err != nil {
				return economics.Valuation{}, err
			}
			refs = append(refs, ref)
		}
	}
	return economics.Valuation{
		Version: economics.ValuationVersionV2, Perspective: input.Perspective, Basis: input.Basis,
		Subject: input.Subject, Scope: input.Scope, InputObservations: refs,
		Completeness: economics.CompletenessPartial, CreatedAt: time.Unix(1_700_090_000, 0).UTC(),
	}, nil
}

type economicJobRunnerTestReconcileCall struct {
	Work    EconomicRevisionWork
	Outputs []EconomicJobDependencyOutput
}

type economicJobRunnerTestReconciler struct {
	mu    sync.Mutex
	calls []economicJobRunnerTestReconcileCall
	errs  []error
}

func (r *economicJobRunnerTestReconciler) ReconcileJob(_ context.Context, work EconomicRevisionWork, outputs []EconomicJobDependencyOutput) (*EconomicReconciliation, error) {
	r.mu.Lock()
	call := len(r.calls)
	r.calls = append(r.calls, economicJobRunnerTestReconcileCall{Work: work, Outputs: append([]EconomicJobDependencyOutput(nil), outputs...)})
	errs := append([]error(nil), r.errs...)
	r.mu.Unlock()
	if call < len(errs) && errs[call] != nil {
		return nil, errs[call]
	}
	return &EconomicReconciliation{
		Subject: work.Subject, Scope: work.Input.Scope, Basis: work.Input.Basis,
		InputSetHash: work.InputSetHash, ResultJSON: json.RawMessage(`{"status":"matched"}`),
		CreatedAt: time.Unix(1_700_090_100, 0).UTC(),
	}, nil
}

func economicJobRunnerTestConfig(store *economicJobRunnerTestStore, rater PostUsageRater, reconciler EconomicJobReconciler) EconomicJobRunnerConfig {
	return EconomicJobRunnerConfig{
		Queue: store, Backlog: store, Results: store, Dependencies: store, Reconciliations: store,
		Rater: rater, Reconciler: reconciler, Owner: "runner-test", Batch: 8,
	}
}

func newEconomicJobRunnerTest(t *testing.T, cfg EconomicJobRunnerConfig) *EconomicJobRunner {
	t.Helper()
	runner, err := NewEconomicJobRunner(cfg)
	require.NoError(t, err)
	return runner
}

func economicJobRunnerTestReconciliationWork(t *testing.T, dependency EconomicRevisionWork) EconomicRevisionWork {
	t.Helper()
	work := economicRevisionTestWork(t, EconomicQueueProvider, 9, "9")
	work.Kind = EconomicWorkKindReconciliation
	work.HeadKey = "economic-job-runner-reconciliation-head"
	normalized, err := dependency.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	reconciliationDependency, err := NewEconomicJobDependency(EconomicWorkKindForQueue(normalized.Queue), identity)
	require.NoError(t, err)
	work.Dependencies = []EconomicJobDependency{reconciliationDependency}
	return work
}

func TestEconomicJobRunnerCustomerProceedsWhileProviderBacklogIncomplete(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	customer := economicRevisionTestWork(t, EconomicQueueCustomer, 1, "3")
	missingDependency := economicRevisionTestWork(t, EconomicQueueProvider, 1, "4")
	reconciliation := economicJobRunnerTestReconciliationWork(t, missingDependency)
	store.appendWork(t, customer)
	// The provider rating output is deliberately not durable yet, so the
	// reconciliation job stays dependency-blocked provider backlog.
	store.appendWork(t, reconciliation)
	rater := &economicJobRunnerTestRater{}
	reconciler := &economicJobRunnerTestReconciler{}
	runner := newEconomicJobRunnerTest(t, economicJobRunnerTestConfig(store, rater, reconciler))

	customerSummary, err := runner.RunOnce(context.Background(), EconomicQueueCustomer)
	require.NoError(t, err)
	require.Equal(t, 1, customerSummary.Completed)
	require.Zero(t, customerSummary.Retried)
	require.Len(t, rater.calls, 1)
	require.Zero(t, customerSummary.Backlog.Pending)

	providerSummary, err := runner.RunOnce(context.Background(), EconomicQueueProvider)
	require.NoError(t, err)
	require.Zero(t, providerSummary.Claimed)
	require.Zero(t, providerSummary.Completed)
	require.Equal(t, 1, providerSummary.Backlog.Pending)
	require.Equal(t, 1, providerSummary.Backlog.IncompleteDependencies)
	require.Len(t, rater.calls, 1)
	require.Empty(t, reconciler.calls)
}

func TestEconomicJobRunnerProviderRatingPromotesReconciliation(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	providerRating := economicRevisionTestWork(t, EconomicQueueProvider, 1, "4")
	reconciliation := economicJobRunnerTestReconciliationWork(t, providerRating)
	ratingIdentity := store.appendWork(t, providerRating)
	reconciliationIdentity := store.appendWork(t, reconciliation)
	rater := &economicJobRunnerTestRater{}
	reconciler := &economicJobRunnerTestReconciler{}
	runner := newEconomicJobRunnerTest(t, economicJobRunnerTestConfig(store, rater, reconciler))

	first, err := runner.RunOnce(context.Background(), EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, first.Completed)
	require.Equal(t, 1, first.Backlog.Pending)
	require.Zero(t, first.Backlog.IncompleteDependencies)

	promoted, err := runner.RunOnce(context.Background(), EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, promoted.Completed)
	require.Equal(t, 2, promoted.Backlog.Completed)
	require.Zero(t, promoted.Backlog.IncompleteDependencies)
	require.Len(t, reconciler.calls, 1)
	require.Len(t, reconciler.calls[0].Outputs, 1)
	require.Equal(t, ratingIdentity.ValuationKey(), reconciler.calls[0].Outputs[0].Valuation.ID)
	require.Equal(t, reconciliation.Dependencies[0], reconciler.calls[0].Outputs[0].Dependency)
	require.Equal(t, 1, store.reconciliationCount())
	probed, err := store.HasEconomicRevisionReconciliation(context.Background(), reconciliationIdentity)
	require.NoError(t, err)
	require.True(t, probed)
}

func TestEconomicJobRunnerInterruptionAfterOutputBeforeCompleteReplaysOnce(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	work := economicRevisionTestWork(t, EconomicQueueCustomer, 1, "3")
	identity := store.appendWork(t, work)
	store.completeFailures[identity.Key()] = 1
	rater := &economicJobRunnerTestRater{}
	runner := newEconomicJobRunnerTest(t, economicJobRunnerTestConfig(store, rater, &economicJobRunnerTestReconciler{}))

	first, err := runner.RunOnce(context.Background(), EconomicQueueCustomer)
	require.NoError(t, err)
	require.Equal(t, 1, first.Retried)
	require.Zero(t, first.Completed)
	require.Len(t, rater.calls, 1)
	require.Equal(t, 1, store.resultCount())
	require.Equal(t, EconomicWorkStatusPending, store.status(identity.Key()))
	require.Equal(t, EconomicWorkReasonPersistenceFailure, store.retryReason(identity.Key()))

	second, err := runner.RunOnce(context.Background(), EconomicQueueCustomer)
	require.NoError(t, err)
	require.Equal(t, 1, second.Completed)
	require.Len(t, rater.calls, 1, "a durable output must not be recomputed")
	require.Equal(t, 1, store.resultCount())
}

func TestEconomicJobRunnerDuplicateWorkerReplayFinishesOnce(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	work := economicRevisionTestWork(t, EconomicQueueProvider, 1, "4")
	identity := store.appendWork(t, work)
	store.completeClaimLost[identity.Key()] = 1
	rater := &economicJobRunnerTestRater{}
	first := newEconomicJobRunnerTest(t, economicJobRunnerTestConfig(store, rater, &economicJobRunnerTestReconciler{}))
	firstSummary, err := first.RunOnce(context.Background(), EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, firstSummary.Superseded)
	require.Zero(t, firstSummary.Completed)
	require.Equal(t, 1, store.resultCount())

	store.expireLeases()
	secondConfig := economicJobRunnerTestConfig(store, rater, &economicJobRunnerTestReconciler{})
	secondConfig.Owner = "runner-second"
	second := newEconomicJobRunnerTest(t, secondConfig)
	secondSummary, err := second.RunOnce(context.Background(), EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, secondSummary.Completed)
	require.Len(t, rater.calls, 1, "replay must finish the durable output without re-rating")
	require.Equal(t, 1, store.resultCount())
	require.Equal(t, EconomicWorkStatusCompleted, store.status(identity.Key()))
}

func TestEconomicJobRunnerStaleFenceCompletionIsSuperseded(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	work := economicRevisionTestWork(t, EconomicQueueCustomer, 1, "3")
	identity := store.appendWork(t, work)
	rater := &economicJobRunnerTestRater{}
	rater.hook = func() {
		store.expireLeases()
		claims, err := store.ClaimEconomicRevisionWorkBatch(context.Background(), EconomicQueueCustomer, "runner-other", time.Minute, 1)
		require.NoError(t, err)
		require.Len(t, claims, 1)
	}
	runner := newEconomicJobRunnerTest(t, economicJobRunnerTestConfig(store, rater, &economicJobRunnerTestReconciler{}))
	summary, err := runner.RunOnce(context.Background(), EconomicQueueCustomer)
	require.NoError(t, err)
	require.Equal(t, 1, summary.Superseded)
	require.Zero(t, summary.Completed)
	require.Equal(t, 1, store.resultCount())
	state := store.state(identity.Key())
	require.Equal(t, EconomicWorkStatusProcessing, state.status)
	require.Equal(t, "runner-other", state.owner)
}

func TestEconomicJobRunnerTransientRetryVersusTerminalFailure(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	transient := economicRevisionTestWork(t, EconomicQueueCustomer, 1, "3")
	terminal := economicRevisionTestWork(t, EconomicQueueCustomer, 2, "5")
	transientIdentity := store.appendWork(t, transient)
	terminalIdentity := store.appendWork(t, terminal)
	rater := &economicJobRunnerTestRater{errs: []error{
		errors.New("transient rater failure"),
		fmt.Errorf("%w: invalid rating input", ErrInvalidEconomicRevision),
	}}
	cfg := economicJobRunnerTestConfig(store, rater, &economicJobRunnerTestReconciler{})
	cfg.RetryBackoff = time.Minute
	runner := newEconomicJobRunnerTest(t, cfg)

	before := time.Now().UTC()
	summary, err := runner.RunOnce(context.Background(), EconomicQueueCustomer)
	require.NoError(t, err)
	require.Equal(t, 1, summary.Retried)
	require.Equal(t, 1, summary.Failed)
	require.Equal(t, EconomicWorkStatusPending, store.status(transientIdentity.Key()))
	require.Equal(t, EconomicWorkReasonRaterFailure, store.retryReason(transientIdentity.Key()))
	require.True(t, store.nextAttempt(transientIdentity.Key()).After(before))
	require.Equal(t, EconomicWorkStatusFailed, store.status(terminalIdentity.Key()))
	require.Equal(t, EconomicWorkReasonPermanentFailure, store.retryReason(terminalIdentity.Key()))
}

func TestEconomicJobRunnerMissingDependencyRetriesWithDependencyPending(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	providerRating := economicRevisionTestWork(t, EconomicQueueProvider, 1, "4")
	reconciliation := economicJobRunnerTestReconciliationWork(t, providerRating)
	reconciliationIdentity := store.appendWork(t, reconciliation)
	store.skipDependencyGate = true
	rater := &economicJobRunnerTestRater{}
	runner := newEconomicJobRunnerTest(t, economicJobRunnerTestConfig(store, rater, &economicJobRunnerTestReconciler{}))

	summary, err := runner.RunOnce(context.Background(), EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, summary.Retried)
	require.Equal(t, EconomicWorkReasonDependencyPending, store.retryReason(reconciliationIdentity.Key()))
	require.Empty(t, rater.calls)
}

func TestEconomicJobRunnerCancellationReleasesClaim(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	work := economicRevisionTestWork(t, EconomicQueueCustomer, 1, "3")
	identity := store.appendWork(t, work)
	ctx, cancel := context.WithCancel(context.Background())
	rater := &economicJobRunnerTestRater{hook: cancel}
	runner := newEconomicJobRunnerTest(t, economicJobRunnerTestConfig(store, rater, &economicJobRunnerTestReconciler{}))

	summary, err := runner.RunOnce(ctx, EconomicQueueCustomer)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, summary.Retried)
	require.Zero(t, summary.Completed)
	require.Equal(t, EconomicWorkStatusPending, store.status(identity.Key()))
	require.Equal(t, EconomicWorkReasonLeaseExpired, store.retryReason(identity.Key()))
	require.Zero(t, store.resultCount())
}

func TestEconomicJobRunnerBoundedBatch(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	for i := 1; i <= 5; i++ {
		store.appendWork(t, economicRevisionTestWork(t, EconomicQueueProvider, uint64(i), string(rune('0'+i))))
	}
	rater := &economicJobRunnerTestRater{}
	cfg := economicJobRunnerTestConfig(store, rater, &economicJobRunnerTestReconciler{})
	cfg.Batch = 2
	runner := newEconomicJobRunnerTest(t, cfg)

	first, err := runner.RunOnce(context.Background(), EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 2, first.Claimed)
	require.Equal(t, 2, first.Completed)
	require.Equal(t, 3, first.Backlog.Pending)
	second, err := runner.RunOnce(context.Background(), EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 2, second.Claimed)
	require.Equal(t, 2, second.Completed)
	require.Equal(t, 1, second.Backlog.Pending)
	require.Len(t, rater.calls, 4)
}

type economicJobRunnerResultsOnly struct{}

func (economicJobRunnerResultsOnly) AppendEconomicRevisionResult(context.Context, EconomicRevisionWork, EconomicRevisionResult) error {
	return nil
}

func TestEconomicJobRunnerFailureVocabularyIsClosed(t *testing.T) {
	t.Parallel()
	require.Len(t, AllEconomicJobFailureStages(), 8)
	for _, stage := range AllEconomicJobFailureStages() {
		require.NoError(t, stage.Validate())
	}
	require.ErrorIs(t, EconomicJobFailureStage(99).Validate(), ErrInvalidEconomicJobRunner)
	require.NoError(t, EconomicJobFailureRetry.Validate())
	require.NoError(t, EconomicJobFailureTerminal.Validate())
	require.ErrorIs(t, EconomicJobFailureDisposition(99).Validate(), ErrInvalidEconomicJobRunner)

	missing := ClassifyEconomicJobFailure(EconomicJobFailureStageDependencyLoad, fmt.Errorf("%w: x", ErrEconomicRevisionDependencyOutputMissing))
	require.Equal(t, EconomicWorkReasonDependencyPending, missing.Reason)
	require.False(t, missing.Terminal())
	canceled := ClassifyEconomicJobFailure(EconomicJobFailureStageRate, context.Canceled)
	require.Equal(t, EconomicWorkReasonLeaseExpired, canceled.Reason)
	require.False(t, canceled.Terminal())
	deterministic := ClassifyEconomicJobFailure(EconomicJobFailureStageRate, fmt.Errorf("%w: x", ErrEconomicRevisionInputMismatch))
	require.Equal(t, EconomicWorkReasonPermanentFailure, deterministic.Reason)
	require.True(t, deterministic.Terminal())
	for stage, want := range map[EconomicJobFailureStage]EconomicWorkReason{
		EconomicJobFailureStageRate:           EconomicWorkReasonRaterFailure,
		EconomicJobFailureStageReconcile:      EconomicWorkReasonReconcilerFailure,
		EconomicJobFailureStageProbe:          EconomicWorkReasonPersistenceFailure,
		EconomicJobFailureStageDependencyLoad: EconomicWorkReasonPersistenceFailure,
		EconomicJobFailureStagePersist:        EconomicWorkReasonPersistenceFailure,
		EconomicJobFailureStageFinalize:       EconomicWorkReasonPersistenceFailure,
		EconomicJobFailureStageClaim:          EconomicWorkReasonTransientFailure,
		EconomicJobFailureStageNormalize:      EconomicWorkReasonTransientFailure,
	} {
		class := ClassifyEconomicJobFailure(stage, errors.New("transient"))
		require.Equal(t, want, class.Reason, stage.String())
		require.False(t, class.Terminal(), stage.String())
	}
}

func TestEconomicJobRunnerRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	rater := &economicJobRunnerTestRater{}
	reconciler := &economicJobRunnerTestReconciler{}
	_, err := NewEconomicJobRunner(EconomicJobRunnerConfig{})
	require.ErrorIs(t, err, ErrInvalidEconomicJobRunner)
	_, err = NewEconomicJobRunner(EconomicJobRunnerConfig{Queue: store, Rater: rater, Reconciler: reconciler})
	require.ErrorIs(t, err, ErrInvalidEconomicJobRunner)
	_, err = NewEconomicJobRunner(EconomicJobRunnerConfig{
		Queue: store, Backlog: store, Results: economicJobRunnerResultsOnly{}, Dependencies: store, Reconciliations: store,
		Rater: rater, Reconciler: reconciler,
	})
	require.ErrorIs(t, err, ErrInvalidEconomicJobRunner, "result store without a replay probe must be rejected")
	_, err = NewEconomicJobRunner(EconomicJobRunnerConfig{
		Queue: store, Backlog: store, Results: store, Dependencies: store, Reconciliations: store,
		Rater: rater, Reconciler: reconciler, Batch: MaxEconomicRevisionClaimBatchSize + 1,
	})
	require.ErrorIs(t, err, ErrInvalidEconomicJobRunner)
	_, err = NewEconomicJobRunner(EconomicJobRunnerConfig{
		Queue: store, Backlog: store, Results: store, Dependencies: store, Reconciliations: store,
		Rater: rater, Reconciler: reconciler, RetryBackoff: -time.Second,
	})
	require.ErrorIs(t, err, ErrInvalidEconomicJobRunner)
}
