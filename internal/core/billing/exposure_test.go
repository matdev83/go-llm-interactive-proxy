package billing

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

func exposureUSD(nano int64) Money {
	return Money{Nano: nano, Currency: "USD"}
}

func exposurePrepaid(balance int64) Account {
	return Account{
		ID: "acct", Currency: "USD", Mode: AccountPrepaid, State: AccountReady,
		BalanceNano: balance, Version: 1,
	}
}

func exposurePostpaid(balance, creditLimit int64) Account {
	return Account{
		ID: "acct", Currency: "USD", Mode: AccountPostpaid, State: AccountReady,
		CreditLimit: creditLimit, BalanceNano: balance, Version: 1,
	}
}

func exposureAdmit(callID string, max int64) AdmitExposureInput {
	return AdmitExposureInput{
		AccountID:       "acct",
		CallID:          callID,
		Max:             exposureUSD(max),
		PricingRef:      VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef: VersionRef{ID: "policy", Version: "v1"},
		Now:             time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC),
	}
}

func TestSettledHeadroomEqualsBalanceMinusCreditFloor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		acct Account
		want int64
	}{
		{name: "prepaid floor is zero", acct: exposurePrepaid(100), want: 100},
		{name: "prepaid zero balance", acct: exposurePrepaid(0), want: 0},
		{name: "prepaid negative balance", acct: exposurePrepaid(-20), want: -20},
		{
			name: "postpaid floor is negative credit limit",
			acct: exposurePostpaid(-35, 100),
			want: 65,
		},
		{
			name: "postpaid zero balance uses full credit limit",
			acct: exposurePostpaid(0, 80),
			want: 80,
		},
		{
			name: "postpaid positive balance adds to credit headroom",
			acct: exposurePostpaid(25, 50),
			want: 75,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.acct.Mode == AccountPrepaid && tt.acct.CreditFloorNano() != 0 {
				t.Fatalf("prepaid CreditFloorNano = %d, want 0", tt.acct.CreditFloorNano())
			}
			if tt.acct.Mode == AccountPostpaid && tt.acct.CreditFloorNano() != -tt.acct.CreditLimit {
				t.Fatalf("postpaid CreditFloorNano = %d, want %d", tt.acct.CreditFloorNano(), -tt.acct.CreditLimit)
			}
			got, err := SettledHeadroom(tt.acct)
			if err != nil {
				t.Fatal(err)
			}
			if got.Nano != tt.want || got.Currency != "USD" {
				t.Fatalf("SettledHeadroom = %+v, want %d USD", got, tt.want)
			}
		})
	}
}

func TestSettledHeadroomIgnoresReservedNanoAndDoesNotConsultExposure(t *testing.T) {
	t.Parallel()
	acct := exposurePrepaid(100)
	acct.State = AccountReconcileRequired
	acct.ReservedNano = 999
	got, err := SettledHeadroom(acct)
	if err != nil {
		t.Fatal(err)
	}
	if got.Nano != 100 {
		t.Fatalf("SettledHeadroom must ignore reserved_nano: got %d", got.Nano)
	}
	spendable, err := acct.SpendableNano()
	if err != nil {
		t.Fatal(err)
	}
	if spendable != got.Nano {
		t.Fatalf("SpendableNano must also ignore reserved_nano: spendable=%d headroom=%d", spendable, got.Nano)
	}
	ready := exposurePrepaid(100)
	ready.ReservedNano = 1
	if _, err := SettledHeadroom(ready); !errors.Is(err, ErrAccountInvalid) {
		t.Fatalf("ready+reserved SettledHeadroom = %v, want ErrAccountInvalid", err)
	}
}

func TestOpenExposureSumsImmutableOpenMaxAmounts(t *testing.T) {
	t.Parallel()
	openA := CallExposure{AccountID: "acct", CallID: "call-a", Max: exposureUSD(40), Status: ExposureOpen}
	openB := CallExposure{AccountID: "acct", CallID: "call-b", Max: exposureUSD(15), Status: ExposureOpen}
	closed := CallExposure{
		AccountID: "acct", CallID: "call-c", Max: exposureUSD(99), Status: ExposureClosed,
		ClosedAt: time.Date(2026, 8, 14, 13, 0, 0, 0, time.UTC),
	}
	got, err := OpenExposure("USD", []CallExposure{openA, closed, openB})
	if err != nil {
		t.Fatal(err)
	}
	if got.Nano != 55 || got.Currency != "USD" {
		t.Fatalf("OpenExposure = %+v, want 55 USD from open rows only", got)
	}
	empty, err := OpenExposure("USD", nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Nano != 0 || empty.Currency != "USD" {
		t.Fatalf("empty OpenExposure = %+v, want 0 USD", empty)
	}
}

func TestSafetyMarginIsHeadroomMinusOpenExposure(t *testing.T) {
	t.Parallel()
	acct := exposurePrepaid(100)
	acct.State = AccountReconcileRequired
	acct.ReservedNano = 70
	open := []CallExposure{
		{AccountID: "acct", CallID: "call-a", Max: exposureUSD(40), Status: ExposureOpen},
		{AccountID: "acct", CallID: "call-b", Max: exposureUSD(25), Status: ExposureOpen},
	}
	got, err := SafetyMargin(acct, open)
	if err != nil {
		t.Fatal(err)
	}
	if got.Nano != 35 || got.Currency != "USD" {
		t.Fatalf("SafetyMargin = %+v, want 35 USD (100-0-65), reserved_nano must not participate", got)
	}
}

func TestEvaluateAdmitRequiresSettledHeadroomCoversOpenPlusNewMax(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		acct      Account
		openNanos []int64
		newMax    int64
		wantErr   error
	}{
		{name: "prepaid exact remaining headroom", acct: exposurePrepaid(100), openNanos: []int64{60}, newMax: 40},
		{name: "prepaid rejects one nano over", acct: exposurePrepaid(100), openNanos: []int64{60}, newMax: 41, wantErr: ErrExposureInsufficient},
		{name: "prepaid zero-headroom free call", acct: exposurePrepaid(0), newMax: 0},
		{name: "prepaid zero-headroom paid call", acct: exposurePrepaid(0), newMax: 1, wantErr: ErrExposureInsufficient},
		{name: "postpaid uses Balance minus CreditFloor", acct: exposurePostpaid(-20, 100), newMax: 80},
		{name: "postpaid rejects beyond credit headroom", acct: exposurePostpaid(-20, 100), newMax: 81, wantErr: ErrExposureInsufficient},
		{name: "two overlapping calls without concurrency=1", acct: exposurePrepaid(100), openNanos: []int64{40}, newMax: 40},
		{name: "ready reserved_nano fails closed before admit", acct: func() Account {
			a := exposurePrepaid(100)
			a.ReservedNano = 999
			return a
		}(), newMax: 100, wantErr: ErrAccountInvalid},
		{name: "reconcile_required reserved_nano blocked as not ready", acct: func() Account {
			a := exposurePrepaid(100)
			a.State = AccountReconcileRequired
			a.ReservedNano = 999
			return a
		}(), newMax: 100, wantErr: ErrAccountNotReady},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			open := make([]CallExposure, 0, len(tt.openNanos))
			for i, nano := range tt.openNanos {
				open = append(open, CallExposure{
					AccountID: tt.acct.ID, CallID: fmt.Sprintf("open-%d", i),
					Max: exposureUSD(nano), Status: ExposureOpen,
				})
			}
			original := tt.acct
			got, err := EvaluateAdmit(tt.acct, open, exposureAdmit("new-call", tt.newMax))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("EvaluateAdmit = %v, want %v", err, tt.wantErr)
				}
				if original != tt.acct {
					t.Fatalf("failed admit mutated account: %+v", tt.acct)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Max.Nano != tt.newMax || got.Status != ExposureOpen || !got.IsOpen() {
				t.Fatalf("admitted exposure = %+v", got)
			}
			if got.Basis.BalanceNano != original.BalanceNano || got.Basis.CreditFloorNano != original.CreditFloorNano() {
				t.Fatalf("admission basis must snapshot financial state: %+v", got.Basis)
			}
			if tt.acct != original {
				t.Fatalf("admit must not mutate the account: %+v", tt.acct)
			}
		})
	}
}

func TestCallExposureAmountDoesNotMutateWhileOpen(t *testing.T) {
	t.Parallel()
	acct := exposurePrepaid(100)
	got, err := EvaluateAdmit(acct, nil, exposureAdmit("call-1", 40))
	if err != nil {
		t.Fatal(err)
	}
	originalMax := got.Max
	got.Max.Nano = 1
	if originalMax.Nano != 40 {
		t.Fatal("Money is a value; stored max snapshot must remain 40")
	}
	again, err := EvaluateAdmit(acct, []CallExposure{{
		AccountID: "acct", CallID: "call-1", Max: originalMax, Status: ExposureOpen,
		PricingRef:      VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef: VersionRef{ID: "policy", Version: "v1"},
		Fingerprint:     got.Fingerprint,
	}}, exposureAdmit("call-1", 40))
	if err != nil {
		t.Fatal(err)
	}
	if again.Max.Nano != 40 {
		t.Fatalf("replayed open exposure mutated max: %+v", again)
	}
	settled, err := EvaluateSettle(acct, []CallExposure{func() CallExposure {
		e := got
		e.Max = originalMax
		return e
	}()}, SettleExposureInput{CallID: "call-1", Actual: exposureUSD(10), Now: got.CreatedAt.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Exposure.Max.Nano != 40 || settled.Exposure.Status != ExposureClosed {
		t.Fatalf("close must retain original max: %+v", settled.Exposure)
	}
}

func TestEvaluateSettleSafetyMarginDoesNotDecreaseWhenActualAtMostMax(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		acct   Account
		max    int64
		actual int64
	}{
		{name: "prepaid actual below max returns unused headroom", acct: exposurePrepaid(100), max: 40, actual: 10},
		{name: "prepaid actual equals max leaves margin unchanged", acct: exposurePrepaid(100), max: 40, actual: 40},
		{name: "prepaid zero actual still closes exposure", acct: exposurePrepaid(100), max: 40, actual: 0},
		{name: "postpaid actual below max", acct: exposurePostpaid(0, 80), max: 50, actual: 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			admitted, err := EvaluateAdmit(tt.acct, nil, exposureAdmit("call-1", tt.max))
			if err != nil {
				t.Fatal(err)
			}
			before, err := SafetyMargin(tt.acct, []CallExposure{admitted})
			if err != nil {
				t.Fatal(err)
			}
			result, err := EvaluateSettle(tt.acct, []CallExposure{admitted}, SettleExposureInput{
				CallID: "call-1", Actual: exposureUSD(tt.actual), Now: admitted.CreatedAt.Add(time.Second),
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.SafetyMarginAfter.Nano < result.SafetyMarginBefore.Nano {
				t.Fatalf("SafetyMargin decreased: before=%d after=%d", result.SafetyMarginBefore.Nano, result.SafetyMarginAfter.Nano)
			}
			wantGain := tt.max - tt.actual
			if result.SafetyMarginAfter.Nano != result.SafetyMarginBefore.Nano+wantGain {
				t.Fatalf("SafetyMarginAfter = %d, want %d + %d", result.SafetyMarginAfter.Nano, result.SafetyMarginBefore.Nano, wantGain)
			}
			if result.SafetyMarginBefore.Nano != before.Nano {
				t.Fatalf("SafetyMarginBefore = %d, want %d", result.SafetyMarginBefore.Nano, before.Nano)
			}
			if result.Account.BalanceNano != tt.acct.BalanceNano-tt.actual {
				t.Fatalf("balance after settle = %d, want %d", result.Account.BalanceNano, tt.acct.BalanceNano-tt.actual)
			}
			if result.Account.ReservedNano != tt.acct.ReservedNano {
				t.Fatalf("settle math must not mutate reserved_nano: %+v", result.Account)
			}
			if tt.acct.BalanceNano != result.Account.BalanceNano+tt.actual {
				t.Fatalf("EvaluateSettle must not mutate the input account: %+v", tt.acct)
			}
		})
	}
}

func TestEvaluateSettleRejectsActualAboveMax(t *testing.T) {
	t.Parallel()
	acct := exposurePrepaid(100)
	admitted, err := EvaluateAdmit(acct, nil, exposureAdmit("call-1", 40))
	if err != nil {
		t.Fatal(err)
	}
	_, err = EvaluateSettle(acct, []CallExposure{admitted}, SettleExposureInput{
		CallID: "call-1", Actual: exposureUSD(41),
	})
	if !errors.Is(err, ErrExposureActualExceedsMax) {
		t.Fatalf("actual > max = %v, want ErrExposureActualExceedsMax", err)
	}
	if acct.BalanceNano != 100 {
		t.Fatalf("rejected settle mutated balance: %+v", acct)
	}
}

func TestExposureArithmeticOverflowAndInvalidMoneyFailClosed(t *testing.T) {
	t.Parallel()
	t.Run("open exposure sum overflow", func(t *testing.T) {
		t.Parallel()
		half := CallExposure{AccountID: "acct", CallID: "a", Max: exposureUSD(math.MaxInt64/2 + 1), Status: ExposureOpen}
		other := CallExposure{AccountID: "acct", CallID: "b", Max: exposureUSD(math.MaxInt64/2 + 1), Status: ExposureOpen}
		if _, err := OpenExposure("USD", []CallExposure{half, other}); !errors.Is(err, ErrMoneyOverflow) {
			t.Fatalf("sum overflow = %v, want ErrMoneyOverflow", err)
		}
	})
	t.Run("settled headroom overflow", func(t *testing.T) {
		t.Parallel()
		acct := exposurePostpaid(math.MaxInt64, 1)
		if _, err := SettledHeadroom(acct); !errors.Is(err, ErrMoneyOverflow) {
			t.Fatalf("headroom overflow = %v, want ErrMoneyOverflow", err)
		}
	})
	t.Run("safety margin overflow", func(t *testing.T) {
		t.Parallel()
		acct := exposurePrepaid(math.MinInt64)
		open := []CallExposure{{AccountID: "acct", CallID: "a", Max: exposureUSD(1), Status: ExposureOpen}}
		if _, err := SafetyMargin(acct, open); !errors.Is(err, ErrMoneyOverflow) {
			t.Fatalf("margin overflow = %v, want ErrMoneyOverflow", err)
		}
	})
	t.Run("invalid max currency", func(t *testing.T) {
		t.Parallel()
		in := exposureAdmit("call-1", 1)
		in.Max.Currency = ""
		if _, err := EvaluateAdmit(exposurePrepaid(100), nil, in); !errors.Is(err, ErrMoneyInvalid) {
			t.Fatalf("empty currency = %v, want ErrMoneyInvalid", err)
		}
	})
	t.Run("currency mismatch", func(t *testing.T) {
		t.Parallel()
		in := exposureAdmit("call-1", 1)
		in.Max.Currency = "EUR"
		if _, err := EvaluateAdmit(exposurePrepaid(100), nil, in); !errors.Is(err, ErrMoneyCurrencyMismatch) {
			t.Fatalf("currency mismatch = %v, want ErrMoneyCurrencyMismatch", err)
		}
	})
	t.Run("negative max", func(t *testing.T) {
		t.Parallel()
		if _, err := EvaluateAdmit(exposurePrepaid(100), nil, exposureAdmit("call-1", -1)); !errors.Is(err, ErrExposureInvalid) {
			t.Fatalf("negative max = %v, want ErrExposureInvalid", err)
		}
	})
}

func TestEvaluateAdmitReplayIsIdempotentAndConflictFailsClosed(t *testing.T) {
	t.Parallel()
	acct := exposurePrepaid(100)
	first, err := EvaluateAdmit(acct, nil, exposureAdmit("call-1", 40))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := EvaluateAdmit(acct, []CallExposure{first}, exposureAdmit("call-1", 40))
	if err != nil {
		t.Fatal(err)
	}
	if replay.Fingerprint != first.Fingerprint || replay.Max != first.Max {
		t.Fatalf("idempotent replay drifted: first=%+v replay=%+v", first, replay)
	}
	conflict := exposureAdmit("call-1", 41)
	if _, err := EvaluateAdmit(acct, []CallExposure{first}, conflict); !errors.Is(err, ErrExposureConflict) {
		t.Fatalf("payload conflict = %v, want ErrExposureConflict", err)
	}
	closed := first
	closed.Status = ExposureClosed
	closed.ClosedAt = first.CreatedAt.Add(time.Minute)
	closedReplay, err := EvaluateAdmit(acct, []CallExposure{closed}, exposureAdmit("call-1", 40))
	if err != nil {
		t.Fatalf("closed same-fingerprint replay = %v, want idempotent success", err)
	}
	if closedReplay.Status != ExposureClosed || closedReplay.Fingerprint != closed.Fingerprint || !closedReplay.ClosedAt.Equal(closed.ClosedAt) {
		t.Fatalf("closed replay mutated exposure: got=%+v want=%+v", closedReplay, closed)
	}
	if _, err := EvaluateAdmit(acct, []CallExposure{closed}, exposureAdmit("call-1", 41)); !errors.Is(err, ErrExposureConflict) {
		t.Fatalf("closed conflicting replay = %v, want ErrExposureConflict", err)
	}
}

func TestSafetyMarginInterleavingsCannotPushBalanceBelowCreditFloor(t *testing.T) {
	t.Parallel()
	type op struct {
		admit  bool
		callID string
		max    int64
		actual int64
	}
	tests := []struct {
		name  string
		acct  Account
		floor int64
		ops   []op
	}{
		{
			name:  "prepaid overlapping admits then undershoot settlements",
			acct:  exposurePrepaid(100),
			floor: 0,
			ops: []op{
				{admit: true, callID: "a", max: 60},
				{admit: true, callID: "b", max: 40},
				{callID: "a", actual: 50},
				{callID: "b", actual: 40},
			},
		},
		{
			name:  "prepaid settle first then admit remainder",
			acct:  exposurePrepaid(100),
			floor: 0,
			ops: []op{
				{admit: true, callID: "a", max: 60},
				{callID: "a", actual: 10},
				{admit: true, callID: "b", max: 90},
				{callID: "b", actual: 90},
			},
		},
		{
			name:  "postpaid concurrent exposure against credit floor",
			acct:  exposurePostpaid(0, 100),
			floor: -100,
			ops: []op{
				{admit: true, callID: "a", max: 80},
				{admit: true, callID: "b", max: 20},
				{callID: "b", actual: 5},
				{callID: "a", actual: 80},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			acct := tt.acct
			open := []CallExposure{}
			for _, step := range tt.ops {
				if step.admit {
					got, err := EvaluateAdmit(acct, open, exposureAdmit(step.callID, step.max))
					if err != nil {
						t.Fatalf("admit %s: %v", step.callID, err)
					}
					open = append(open, got)
					continue
				}
				result, err := EvaluateSettle(acct, open, SettleExposureInput{
					CallID: step.callID, Actual: exposureUSD(step.actual),
				})
				if err != nil {
					t.Fatalf("settle %s: %v", step.callID, err)
				}
				if result.SafetyMarginAfter.Nano < result.SafetyMarginBefore.Nano {
					t.Fatalf("settle %s decreased SafetyMargin", step.callID)
				}
				acct = result.Account
				next := make([]CallExposure, 0, len(open))
				for _, e := range open {
					if e.CallID == step.callID {
						next = append(next, result.Exposure)
						continue
					}
					next = append(next, e)
				}
				open = next
				if acct.BalanceNano < tt.floor {
					t.Fatalf("balance %d below credit floor %d", acct.BalanceNano, tt.floor)
				}
			}
			if acct.BalanceNano < tt.floor {
				t.Fatalf("final balance %d below credit floor %d", acct.BalanceNano, tt.floor)
			}
		})
	}
}

func TestEvaluateAdmitDoesNotProduceJournalOrRequireAuthorizationBook(t *testing.T) {
	t.Parallel()
	acct := exposurePrepaid(80)
	got, err := EvaluateAdmit(acct, nil, exposureAdmit("call-1", 20))
	if err != nil {
		t.Fatal(err)
	}
	if got.Max.Currency != "USD" || got.CallID != "call-1" {
		t.Fatalf("exposure = %+v", got)
	}
	if _, err := SettledHeadroom(acct); err != nil {
		t.Fatal(err)
	}
	// Domain admit returns a CallExposure value object only. There is no journal
	// transaction, reserved_nano mutation, or authorization-book posting to inspect.
	if acct.BalanceNano != 80 || acct.ReservedNano != 0 {
		t.Fatalf("admit mutated financial account: %+v", acct)
	}
}
