package billing

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

type workerStore struct {
	mu                sync.Mutex
	records           []TurnUsageRecord
	retryCodes        []string
	unreconciled      int
	settlements       int
	processedState    int
	terminal          int
	reconcileRequired int
	reconcileAccounts []string
	settleErr         error
}

func (s *workerStore) ClaimPending(context.Context, int) ([]TurnUsageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]TurnUsageRecord(nil), s.records...)
	s.records = nil
	return out, nil
}

func (s *workerStore) MarkProcessingRetryable(_ context.Context, _, _, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryCodes = append(s.retryCodes, code)
	return nil
}

func (s *workerStore) MarkProcessingTerminal(context.Context, string, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.terminal++
	return nil
}

func (s *workerStore) MarkProcessingUnreconciledCost(context.Context, string, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unreconciled++
	return nil
}

func (s *workerStore) MarkProcessingProcessed(context.Context, string, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.processedState++
	return nil
}

func (s *workerStore) MarkProcessingInvariantFailure(_ context.Context, record TurnUsageRecord, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.terminal++
	s.reconcileRequired++
	s.reconcileAccounts = append(s.reconcileAccounts, record.AccountID)
	return nil
}

func (s *workerStore) ApplyBillingResult(context.Context, ApplyBillingInput) (Settlement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settleErr != nil {
		return Settlement{}, s.settleErr
	}
	s.settlements++
	return Settlement{}, nil
}

type workerResolver struct {
	input RatingInput
	err   error
}

func (r workerResolver) ResolveRating(context.Context, TurnUsageRecord) (RatingInput, error) {
	return r.input, r.err
}

func workerRecord(t *testing.T) TurnUsageRecord {
	t.Helper()
	record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedYes}, []MoneyEvidence{{NanoUnits: 0, Currency: "USD", Present: true}})
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func overflowRatingInput(t *testing.T, _ TurnUsageRecord) RatingInput {
	t.Helper()
	base := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedYes}, []MoneyEvidence{{NanoUnits: 0, Currency: "USD", Present: true}})
	base.Legs[0].Evidence.InputTokens = Quantity{Value: 2_000_000, Present: true}
	sealed, err := base.Seal()
	if err != nil {
		t.Fatal(err)
	}
	pricing := ratingPricing()
	pricing.InputPerMillionNano = math.MaxInt64
	pricing.FixedCharges = nil
	return RatingInput{
		Record: sealed, Authorization: ratingAuthorization(math.MaxInt64),
		CustomerPricing: pricing, CustomerPolicy: ratingPolicy(ChargeSurfacedTurn),
		OperatorRates: []OperatorRateSnapshot{operatorRate()},
	}
}

func TestPostTurnWorkerClaimsRatesAndSettles(t *testing.T) {
	t.Parallel()
	record := workerRecord(t)
	store := &workerStore{records: []TurnUsageRecord{record}}
	input := RatingInput{
		Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(),
		CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{operatorRate()},
	}
	worker, err := NewPostTurnWorker(store, workerResolver{input: input}, PostTurnWorkerConfig{BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.settlements != 1 || len(store.retryCodes) != 0 || store.unreconciled != 0 {
		t.Fatalf("store state = %+v", store)
	}
}

func TestPostTurnWorkerMarksResolverFailureRetryable(t *testing.T) {
	t.Parallel()
	record := workerRecord(t)
	store := &workerStore{records: []TurnUsageRecord{record}}
	worker, err := NewPostTurnWorker(store, workerResolver{err: errors.New("snapshot unavailable")}, PostTurnWorkerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(context.Background()); err == nil {
		t.Fatal("expected resolver error")
	}
	if len(store.retryCodes) != 1 || store.retryCodes[0] != "rating_input_unavailable" {
		t.Fatalf("retry codes = %v", store.retryCodes)
	}
	if store.settlements != 0 {
		t.Fatal("resolver failure must not settle")
	}
}

func TestPostTurnWorkerLeavesUnreconciledCostUnsettled(t *testing.T) {
	t.Parallel()
	record := workerRecord(t)
	record.Legs[0].Evidence.Cost = MoneyEvidence{Currency: "USD", Present: false}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	store := &workerStore{records: []TurnUsageRecord{sealed}}
	input := RatingInput{
		Record: sealed, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(),
		CustomerPolicy: ratingPolicy(ChargeSurfacedTurn),
	}
	worker, err := NewPostTurnWorker(store, workerResolver{input: input}, PostTurnWorkerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.unreconciled != 1 || store.settlements != 0 {
		t.Fatalf("store state = %+v", store)
	}
}

func TestPostTurnWorkerStartStopIsIdempotent(t *testing.T) {
	t.Parallel()
	store := &workerStore{}
	worker, err := NewPostTurnWorker(store, workerResolver{}, PostTurnWorkerConfig{Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPostTurnWorkerMarksInsufficientSpendableTerminal(t *testing.T) {
	t.Parallel()
	record := workerRecord(t)
	store := &workerStore{records: []TurnUsageRecord{record}, settleErr: ErrInsufficientSpendable}
	input := RatingInput{
		Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(),
		CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{operatorRate()},
	}
	worker, err := NewPostTurnWorker(store, workerResolver{input: input}, PostTurnWorkerConfig{BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(context.Background()); err == nil {
		t.Fatal("expected insufficient spendable error")
	}
	if store.terminal != 1 || len(store.retryCodes) != 0 || store.reconcileRequired != 0 {
		t.Fatalf("insufficient spendable must be terminal without quarantine, terminal=%d reconcile=%d retries=%v", store.terminal, store.reconcileRequired, store.retryCodes)
	}
}

func TestPostTurnWorkerRetriesWhenAccountNotReady(t *testing.T) {
	t.Parallel()
	record := workerRecord(t)
	store := &workerStore{records: []TurnUsageRecord{record}, settleErr: ErrAccountNotReady}
	input := RatingInput{
		Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(),
		CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{operatorRate()},
	}
	worker, err := NewPostTurnWorker(store, workerResolver{input: input}, PostTurnWorkerConfig{BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(context.Background()); err == nil {
		t.Fatal("expected account-not-ready error")
	}
	if store.terminal != 0 || len(store.retryCodes) != 1 || store.retryCodes[0] != "settlement_failed" {
		t.Fatalf("reconcile_required settlement must stay retryable, terminal=%d retries=%v", store.terminal, store.retryCodes)
	}
}

func TestNewPostTurnWorkerRequiresDepsAndDefaults(t *testing.T) {
	t.Parallel()
	if _, err := NewPostTurnWorker(nil, workerResolver{}, PostTurnWorkerConfig{}); err == nil {
		t.Fatal("expected nil store error")
	}
	if _, err := NewPostTurnWorker(&workerStore{}, nil, PostTurnWorkerConfig{}); err == nil {
		t.Fatal("expected nil resolver error")
	}
	worker, err := NewPostTurnWorker(&workerStore{}, workerResolver{}, PostTurnWorkerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if worker.batch != 32 || worker.interval != time.Second {
		t.Fatalf("defaults batch=%d interval=%s, want 32 and 1s", worker.batch, worker.interval)
	}
}

var _ PostTurnStore = (*workerStore)(nil)

func TestPostTurnWorkerPermanentErrorClassification(t *testing.T) {
	t.Parallel()
	record := workerRecord(t)
	successInput := RatingInput{
		Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(),
		CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{operatorRate()},
	}
	mismatchPricing := ratingPricing()
	mismatchPricing.Ref.Version = "other"
	currencyPricing := ratingPricing()
	currencyPricing.Currency = "EUR"

	tests := []struct {
		name          string
		input         RatingInput
		settleErr     error
		wantTerm      bool
		wantReconcile bool
		retryCode     string
	}{
		{
			name:      "snapshot mismatch is terminal",
			input:     RatingInput{Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: mismatchPricing, CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{operatorRate()}},
			wantTerm:  true,
			retryCode: "rating_failed",
		},
		{
			name:      "currency mismatch is terminal",
			input:     RatingInput{Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: currencyPricing, CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{operatorRate()}},
			wantTerm:  true,
			retryCode: "rating_failed",
		},
		{
			name:          "actual charge exceeds authorization quarantines the account",
			input:         RatingInput{Record: record, Authorization: ratingAuthorization(1), CustomerPricing: ratingPricing(), CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{operatorRate()}},
			wantTerm:      true,
			wantReconcile: true,
			retryCode:     "rating_failed",
		},
		{
			name:          "settlement conflict quarantines the account",
			input:         successInput,
			settleErr:     ErrSettlementConflict,
			wantTerm:      true,
			wantReconcile: true,
			retryCode:     "settlement_failed",
		},
		{
			name:          "journal fingerprint quarantines the account",
			input:         successInput,
			settleErr:     ErrJournalFingerprint,
			wantTerm:      true,
			wantReconcile: true,
			retryCode:     "settlement_failed",
		},
		{
			name:      "insufficient spendable is terminal",
			input:     successInput,
			settleErr: ErrInsufficientSpendable,
			wantTerm:  true,
			retryCode: "settlement_failed",
		},
		{
			name:      "account not ready is retryable",
			input:     successInput,
			settleErr: ErrAccountNotReady,
			wantTerm:  false,
			retryCode: "settlement_failed",
		},
		{
			name:      "canceled context is retryable",
			input:     successInput,
			settleErr: context.Canceled,
			wantTerm:  false,
			retryCode: "settlement_failed",
		},
		{
			name:      "rating arithmetic overflow is terminal",
			input:     overflowRatingInput(t, record),
			wantTerm:  true,
			retryCode: "rating_failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := &workerStore{records: []TurnUsageRecord{record}, settleErr: tt.settleErr}
			worker, err := NewPostTurnWorker(store, workerResolver{input: tt.input}, PostTurnWorkerConfig{BatchSize: 1})
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.ProcessOnce(context.Background()); err == nil {
				t.Fatal("expected processing error")
			}
			if tt.wantTerm {
				if store.terminal != 1 || len(store.retryCodes) != 0 {
					t.Fatalf("terminal=%d retries=%v, want terminal", store.terminal, store.retryCodes)
				}
				if tt.wantReconcile {
					if store.reconcileRequired != 1 || len(store.reconcileAccounts) != 1 || store.reconcileAccounts[0] != record.AccountID {
						t.Fatalf("reconcileRequired=%d accounts=%v, want account quarantine", store.reconcileRequired, store.reconcileAccounts)
					}
				} else if store.reconcileRequired != 0 {
					t.Fatalf("reconcileRequired=%d, want no account quarantine", store.reconcileRequired)
				}
				return
			}
			if store.terminal != 0 || len(store.retryCodes) != 1 || store.retryCodes[0] != tt.retryCode {
				t.Fatalf("terminal=%d retries=%v, want retryable %q", store.terminal, store.retryCodes, tt.retryCode)
			}
		})
	}
}
