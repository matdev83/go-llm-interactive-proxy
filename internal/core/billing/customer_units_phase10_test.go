package billing

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestCustomerUnitKeySeparatesTrustedScopesAndComponents(t *testing.T) {
	t.Parallel()

	base := phase10CustomerUnitKey(t, "customer-a", "included", "2026-09", metering.ComponentCredit, metering.UnitCredit)
	variants := []CustomerUnitKey{
		phase10CustomerUnitKey(t, "customer-b", "included", "2026-09", metering.ComponentCredit, metering.UnitCredit),
		phase10CustomerUnitKey(t, "customer-a", "premium", "2026-09", metering.ComponentCredit, metering.UnitCredit),
		phase10CustomerUnitKey(t, "customer-a", "included", "2026-10", metering.ComponentCredit, metering.UnitCredit),
		phase10CustomerUnitKey(t, "customer-a", "included", "2026-09", metering.ComponentToolQuery, metering.UnitCount),
		{
			AccountID: "customer-a", PoolID: "included", PeriodID: "2026-09",
			Component: metering.ComponentKey{
				Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
				Unit: metering.UnitToken, SchemaID: "customer.offer.v1",
			},
		},
	}

	baseIdentity, err := base.IdentityKey()
	if err != nil {
		t.Fatalf("base identity: %v", err)
	}
	for i, variant := range variants {
		variantIdentity, err := variant.IdentityKey()
		if err != nil {
			t.Fatalf("variant %d identity: %v", i, err)
		}
		if variantIdentity == baseIdentity {
			t.Fatalf("variant %d identity collided with base: %q", i, baseIdentity)
		}
	}
}

func TestEvaluateIncludedAllowanceReturnsExactCoverageAndBoundedFallback(t *testing.T) {
	t.Parallel()

	key := phase10CustomerUnitKey(t, "customer-a", "included", "2026-09", metering.ComponentCredit, metering.UnitCredit)
	balance := phase10CustomerUnitBalance(t, key, "100.000", "3.125", "0", "96.875", 7, 11)
	fallback := Money{Nano: 125_000_000, Currency: "USD"}
	decision, err := EvaluateIncludedAllowance(IncludedAllowanceInput{
		Key:                   key,
		Balance:               balance,
		Requested:             phase10CustomerDecimal(t, "5.250"),
		MonetaryFallbackBound: &fallback,
	})
	if err != nil {
		t.Fatalf("EvaluateIncludedAllowance: %v", err)
	}
	if got := decision.Included.CanonicalString(); got != "3125/3" {
		t.Fatalf("included = %s, want 3.125", got)
	}
	if got := decision.Uncovered.CanonicalString(); got != "2125/3" {
		t.Fatalf("uncovered = %s, want 2.125", got)
	}
	if !decision.FallbackRequired {
		t.Fatal("fallback must be required for the uncovered quantity")
	}
	if decision.MonetaryFallbackBound == nil || *decision.MonetaryFallbackBound != fallback {
		t.Fatalf("fallback bound = %+v, want %+v", decision.MonetaryFallbackBound, fallback)
	}
	if decision.Entitlement != CustomerEntitlementComplete {
		t.Fatalf("entitlement = %q, want complete", decision.Entitlement)
	}
}

func TestEvaluateIncludedAllowanceRejectsInsufficientWithoutFallback(t *testing.T) {
	t.Parallel()

	key := phase10CustomerUnitKey(t, "customer-a", "included", "2026-09", metering.ComponentCredit, metering.UnitCredit)
	balance := phase10CustomerUnitBalance(t, key, "10", "2", "0", "8", 1, 1)
	decision, err := EvaluateIncludedAllowance(IncludedAllowanceInput{
		Key:       key,
		Balance:   balance,
		Requested: phase10CustomerDecimal(t, "3"),
	})
	if !errors.Is(err, ErrCustomerUnitInsufficient) {
		t.Fatalf("error = %v, want ErrCustomerUnitInsufficient", err)
	}
	if got := decision.Included.CanonicalString(); got != "2/0" {
		t.Fatalf("included = %s, want known available 2", got)
	}
	if got := decision.Uncovered.CanonicalString(); got != "1/0" {
		t.Fatalf("uncovered = %s, want 1", got)
	}
	if decision.FallbackRequired {
		t.Fatal("fallback must not be claimed without a bound")
	}
}

func TestEvaluateIncludedAllowanceClassifiesMissingAndPartialEntitlement(t *testing.T) {
	t.Parallel()

	key := phase10CustomerUnitKey(t, "customer-a", "included", "2026-09", metering.ComponentCredit, metering.UnitCredit)
	requested := phase10CustomerDecimal(t, "1")
	for _, tc := range []struct {
		name   string
		status CustomerEntitlementStatus
		err    error
	}{
		{name: "missing", status: CustomerEntitlementMissing, err: ErrCustomerUnitEntitlementMissing},
		{name: "partial", status: CustomerEntitlementPartial, err: ErrCustomerUnitEntitlementPartial},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			balance := CustomerUnitBalance{Key: key, Status: tc.status}
			decision, err := EvaluateIncludedAllowance(IncludedAllowanceInput{Key: key, Balance: balance, Requested: requested})
			if !errors.Is(err, tc.err) {
				t.Fatalf("error = %v, want %v", err, tc.err)
			}
			if decision.Entitlement != tc.status {
				t.Fatalf("entitlement = %q, want %q", decision.Entitlement, tc.status)
			}
		})
	}
}

func TestTransitionCustomerUnitBalanceSupportsDebitAndReservationLifecycle(t *testing.T) {
	t.Parallel()

	key := phase10CustomerUnitKey(t, "customer-a", "included", "2026-09", metering.ComponentCredit, metering.UnitCredit)
	balance := phase10CustomerUnitBalance(t, key, "10", "10", "0", "0", 1, 4)
	reserve := phase10CustomerUnitOperation(t, key, CustomerUnitOperationReserve, "3", "reserve-1", 1, 4, nil)
	reserved, err := TransitionCustomerUnitBalance(reserve, balance)
	if err != nil {
		t.Fatalf("reserve transition: %v", err)
	}
	if got := reserved.After.Available.CanonicalString(); got != "7/0" {
		t.Fatalf("available after reserve = %s, want 7", got)
	}
	if got := reserved.After.Reserved.CanonicalString(); got != "3/0" {
		t.Fatalf("reserved after reserve = %s, want 3", got)
	}
	if err := reserved.Decision.Validate(); err != nil {
		t.Fatalf("reserve decision: %v", err)
	}

	commit := phase10CustomerUnitOperation(t, key, CustomerUnitOperationCommit, "3", "reserve-1", reserved.After.Version, 4, nil)
	committed, err := TransitionCustomerUnitBalance(commit, reserved.After)
	if err != nil {
		t.Fatalf("commit transition: %v", err)
	}
	if got := committed.After.Reserved.CanonicalString(); got != "0/0" {
		t.Fatalf("reserved after commit = %s, want 0", got)
	}
	if got := committed.After.Consumed.CanonicalString(); got != "3/0" {
		t.Fatalf("consumed after commit = %s, want 3", got)
	}
	if err := committed.Decision.Validate(); err != nil {
		t.Fatalf("commit decision: %v", err)
	}

	release := phase10CustomerUnitOperation(t, key, CustomerUnitOperationRelease, "2", "reserve-2", committed.After.Version, 4, nil)
	// A release requires units in the reservation row in the driven adapter. The
	// pure balance transition still proves the balance movement and version/fence
	// semantics for an operation that has already passed that reservation check.
	releasedBalance := committed.After
	releasedBalance.Reserved = phase10DecimalPtr(t, "2")
	releasedBalance.Available = phase10DecimalPtr(t, "5")
	released, err := TransitionCustomerUnitBalance(release, releasedBalance)
	if err != nil {
		t.Fatalf("release transition: %v", err)
	}
	if got := released.After.Available.CanonicalString(); got != "7/0" {
		t.Fatalf("available after release = %s, want 7", got)
	}
	if err := released.Decision.Validate(); err != nil {
		t.Fatalf("release decision: %v", err)
	}
}

func TestTransitionCustomerUnitBalanceConsumesKnownUnitsAndReturnsBoundedFallback(t *testing.T) {
	t.Parallel()

	key := phase10CustomerUnitKey(t, "customer-a", "included", "2026-09", metering.ComponentCredit, metering.UnitCredit)
	balance := phase10CustomerUnitBalance(t, key, "3", "1", "0", "2", 4, 9)
	fallback := Money{Nano: 50_000_000, Currency: "USD"}
	op := phase10CustomerUnitOperation(t, key, CustomerUnitOperationDebit, "2", "", 4, 9, &fallback)
	transition, err := TransitionCustomerUnitBalance(op, balance)
	if err != nil {
		t.Fatalf("fallback transition: %v", err)
	}
	if got := transition.AppliedQuantity.CanonicalString(); got != "1/0" {
		t.Fatalf("applied quantity = %s, want 1", got)
	}
	if got := transition.Decision.Uncovered.CanonicalString(); got != "1/0" {
		t.Fatalf("uncovered quantity = %s, want 1", got)
	}
	if !transition.Decision.FallbackRequired || transition.Decision.MonetaryFallbackBound == nil {
		t.Fatal("fallback decision must retain its bound")
	}
	if got := transition.After.Consumed.CanonicalString(); got != "3/0" {
		t.Fatalf("consumed after fallback = %s, want 3", got)
	}
	if transition.After.Version != balance.Version+1 {
		t.Fatalf("version after known-unit debit = %d, want %d", transition.After.Version, balance.Version+1)
	}
}

func TestTransitionCustomerUnitBalanceRejectsStaleVersionAndFence(t *testing.T) {
	t.Parallel()

	key := phase10CustomerUnitKey(t, "customer-a", "included", "2026-09", metering.ComponentCredit, metering.UnitCredit)
	balance := phase10CustomerUnitBalance(t, key, "10", "10", "0", "0", 8, 12)
	for _, tc := range []struct {
		name     string
		expected uint64
		fence    uint64
		want     error
	}{
		{name: "version", expected: 7, fence: 12, want: ErrCustomerUnitStaleVersion},
		{name: "fence", expected: 8, fence: 11, want: ErrCustomerUnitStaleFence},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			op := phase10CustomerUnitOperation(t, key, CustomerUnitOperationDebit, "1", "op-"+tc.name, tc.expected, tc.fence, nil)
			_, err := TransitionCustomerUnitBalance(op, balance)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCustomerUnitOperationRejectsProviderGaugeAuthority(t *testing.T) {
	t.Parallel()

	key := phase10CustomerUnitKey(t, "customer-a", "included", "2026-09", metering.ComponentCredit, metering.UnitCredit)
	op := phase10CustomerUnitOperation(t, key, CustomerUnitOperationDebit, "1", "provider-gauge", 1, 1, nil)
	op.Source = CustomerUnitOperationSource("provider_account_window")
	if err := op.Validate(); !errors.Is(err, ErrCustomerUnitAuthority) {
		t.Fatalf("error = %v, want ErrCustomerUnitAuthority", err)
	}
}

func TestCustomerUnitOperationReplayAndConflictUseStablePayloadIdentity(t *testing.T) {
	t.Parallel()

	key := phase10CustomerUnitKey(t, "customer-a", "included", "2026-09", metering.ComponentCredit, metering.UnitCredit)
	first := phase10CustomerUnitOperation(t, key, CustomerUnitOperationDebit, "2", "op-1", 1, 1, nil)
	replay := first
	replay.ExpectedVersion = 9
	replay.Fence = 20
	if err := CheckCustomerUnitOperationReplay(first, replay); err != nil {
		t.Fatalf("replay check: %v", err)
	}
	conflict := first
	conflict.Quantity = phase10CustomerDecimal(t, "3")
	if err := CheckCustomerUnitOperationReplay(first, conflict); !errors.Is(err, ErrCustomerUnitOperationConflict) {
		t.Fatalf("conflict error = %v, want ErrCustomerUnitOperationConflict", err)
	}
}

func TestApplyCustomerUnitOperationUsesOneAtomicPortCall(t *testing.T) {
	t.Parallel()

	key := phase10CustomerUnitKey(t, "customer-a", "included", "2026-09", metering.ComponentCredit, metering.UnitCredit)
	op := phase10CustomerUnitOperation(t, key, CustomerUnitOperationDebit, "6", "op-1", 1, 1, nil)
	before := phase10CustomerUnitBalance(t, key, "10", "10", "0", "0", 1, 1)
	after := before
	after.Available = phase10DecimalPtr(t, "4")
	after.Consumed = phase10DecimalPtr(t, "6")
	after.Version = 2
	ledger := &phase10AtomicLedger{result: CustomerUnitOperationResult{
		OperationID:       op.OperationID,
		Key:               key,
		Kind:              op.Kind,
		Status:            CustomerUnitOperationApplied,
		Entitlement:       CustomerEntitlementComplete,
		Before:            before,
		After:             after,
		AppliedQuantity:   phase10DecimalPtr(t, "6"),
		UncoveredQuantity: phase10DecimalPtr(t, "0"),
		Fingerprint:       phase10CustomerUnitOperationFingerprint(t, op),
	}}
	result, err := ApplyCustomerUnitOperation(context.Background(), ledger, op)
	if err != nil {
		t.Fatalf("ApplyCustomerUnitOperation: %v", err)
	}
	if ledger.calls != 1 {
		t.Fatalf("atomic port calls = %d, want 1", ledger.calls)
	}
	if result.After.Consumed.CanonicalString() != "6/0" {
		t.Fatalf("consumed = %s, want 6", result.After.Consumed.CanonicalString())
	}
}

func TestConcurrentCustomerUnitOperationsAllowOneSpendAndFenceTheOther(t *testing.T) {
	t.Parallel()

	key := phase10CustomerUnitKey(t, "customer-a", "included", "2026-09", metering.ComponentCredit, metering.UnitCredit)
	ledger := &phase10ConcurrentLedger{balance: phase10CustomerUnitBalance(t, key, "10", "10", "0", "0", 1, 1)}
	first := phase10CustomerUnitOperation(t, key, CustomerUnitOperationDebit, "6", "op-1", 1, 1, nil)
	second := phase10CustomerUnitOperation(t, key, CustomerUnitOperationDebit, "6", "op-2", 1, 1, nil)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, op := range []CustomerUnitOperation{first, second} {
		op := op
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ApplyCustomerUnitOperation(context.Background(), ledger, op)
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	var successes, stale int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrCustomerUnitStaleVersion):
			stale++
		default:
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("successes=%d stale=%d, want one each", successes, stale)
	}
	if got := ledger.balance.Consumed.CanonicalString(); got != "6/0" {
		t.Fatalf("consumed = %s, want one 6-unit debit", got)
	}
	if ledger.calls != 2 {
		t.Fatalf("atomic port calls = %d, want 2", ledger.calls)
	}
}

type phase10AtomicLedger struct {
	calls  int
	result CustomerUnitOperationResult
}

func (l *phase10AtomicLedger) ApplyCustomerUnitOperation(_ context.Context, _ CustomerUnitOperation) (CustomerUnitOperationResult, error) {
	l.calls++
	return l.result, nil
}

type phase10ConcurrentLedger struct {
	mu      sync.Mutex
	balance CustomerUnitBalance
	calls   int
}

func (l *phase10ConcurrentLedger) ApplyCustomerUnitOperation(_ context.Context, op CustomerUnitOperation) (CustomerUnitOperationResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	transition, err := TransitionCustomerUnitBalance(op, l.balance)
	if err != nil {
		return CustomerUnitOperationResult{}, err
	}
	l.balance = transition.After
	return CustomerUnitOperationResult{
		OperationID:       op.OperationID,
		Key:               op.Key,
		Kind:              op.Kind,
		Status:            CustomerUnitOperationApplied,
		Entitlement:       transition.After.Status,
		Before:            transition.Before,
		After:             transition.After,
		AppliedQuantity:   &transition.Decision.Included,
		UncoveredQuantity: &transition.Decision.Uncovered,
		FallbackRequired:  transition.Decision.FallbackRequired,
		FallbackBound:     transition.Decision.MonetaryFallbackBound,
		Fingerprint:       phase10CustomerUnitOperationFingerprintMust(op),
		ReservationID:     op.ReservationID,
	}, nil
}

func phase10CustomerUnitKey(t *testing.T, account, pool, period, component, unit string) CustomerUnitKey {
	t.Helper()
	key := CustomerUnitKey{
		AccountID: account,
		PoolID:    pool,
		PeriodID:  period,
		Component: metering.ComponentKey{
			Direction: metering.DirectionNone,
			Component: component,
			Unit:      unit,
			SchemaID:  "customer.offer.v1",
		},
	}
	if err := key.Validate(); err != nil {
		t.Fatalf("customer unit key: %v", err)
	}
	return key
}

func phase10CustomerUnitBalance(t *testing.T, key CustomerUnitKey, granted, available, reserved, consumed string, version, fence uint64) CustomerUnitBalance {
	t.Helper()
	balance := CustomerUnitBalance{
		Key:       key,
		Status:    CustomerEntitlementComplete,
		Granted:   phase10DecimalPtr(t, granted),
		Available: phase10DecimalPtr(t, available),
		Reserved:  phase10DecimalPtr(t, reserved),
		Consumed:  phase10DecimalPtr(t, consumed),
		Version:   version,
		Fence:     fence,
	}
	if err := balance.Validate(); err != nil {
		t.Fatalf("customer unit balance: %v", err)
	}
	return balance
}

func phase10CustomerUnitOperation(t *testing.T, key CustomerUnitKey, kind CustomerUnitOperationKind, quantity, reservationID string, expectedVersion, fence uint64, fallback *Money) CustomerUnitOperation {
	t.Helper()
	op := CustomerUnitOperation{
		Version:               CustomerUnitOperationVersionV1,
		OperationID:           "customer-op-" + reservationID,
		Key:                   key,
		Kind:                  kind,
		Source:                CustomerUnitOperationSourceRetailSettlement,
		Quantity:              phase10CustomerDecimal(t, quantity),
		ReservationID:         reservationID,
		ExpectedVersion:       expectedVersion,
		Fence:                 fence,
		MonetaryFallbackBound: fallback,
	}
	if kind == CustomerUnitOperationDebit {
		op.ReservationID = ""
		op.OperationID = "customer-op-" + quantity
	}
	if err := op.Validate(); err != nil {
		t.Fatalf("customer unit operation: %v", err)
	}
	return op
}

func phase10CustomerDecimal(t *testing.T, raw string) metering.Decimal {
	t.Helper()
	value, err := metering.ParseDecimal(raw)
	if err != nil {
		t.Fatalf("decimal %q: %v", raw, err)
	}
	return value
}

func phase10DecimalPtr(t *testing.T, raw string) *metering.Decimal {
	t.Helper()
	value := phase10CustomerDecimal(t, raw)
	return &value
}

func phase10CustomerUnitOperationFingerprint(t *testing.T, op CustomerUnitOperation) string {
	t.Helper()
	fingerprint, err := op.SemanticFingerprint()
	if err != nil {
		t.Fatalf("operation fingerprint: %v", err)
	}
	return fingerprint
}

func phase10CustomerUnitOperationFingerprintMust(op CustomerUnitOperation) string {
	fingerprint, _ := op.SemanticFingerprint()
	return fingerprint
}
