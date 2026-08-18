package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestAmountValidateQuantityOnly(t *testing.T) {
	t.Parallel()
	for _, amount := range []Amount{
		{Unit: AmountUnitRequests, Value: 7},
		{Unit: AmountUnitInputTokens, Value: 11},
		{Unit: AmountUnitOutputTokens, Value: 3},
	} {
		if err := amount.Validate(); err != nil {
			t.Fatalf("quantity amount %#v: %v", amount, err)
		}
	}
	if err := (Amount{Unit: AmountUnit("money_nano"), Value: 1}).Validate(); !errors.Is(err, ErrRetiredMonetaryAuthority) {
		t.Fatalf("retired money amount error = %v, want migration error", err)
	}
	if err := (Amount{Unit: AmountUnitRequests, Value: -1}).Validate(); !strings.Contains(err.Error(), "negative") {
		t.Fatalf("negative amount error = %v", err)
	}
}

func TestDimensionKeyDeterministicAndPresenceAware(t *testing.T) {
	t.Parallel()
	first := Dimensions{Principal: scope.Known("principal-a"), Tenant: scope.Known("tenant-a"), Backend: scope.Known("backend-a"), Model: scope.Known("model-a"), PolicyLabels: map[string]scope.Value{"beta": scope.Known("2"), "alpha": scope.Known("1")}}
	second := Dimensions{Principal: scope.Known("principal-a"), Tenant: scope.Known("tenant-a"), Backend: scope.Known("backend-a"), Model: scope.Known("model-a"), PolicyLabels: map[string]scope.Value{"alpha": scope.Known("1"), "beta": scope.Known("2")}}
	if first.Key() != second.Key() {
		t.Fatalf("dimension keys differ: %q != %q", first.Key(), second.Key())
	}
	if (Dimensions{Principal: scope.Known("")}).Key() == (Dimensions{Principal: scope.Unknown()}).Key() {
		t.Fatal("known-empty and unknown keys should differ")
	}
}

func TestFixedWindowBoundsAndKey(t *testing.T) {
	t.Parallel()
	spec := WindowSpec{Algorithm: WindowAlgorithmFixed, Size: time.Hour, Anchor: time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)}
	at := time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC)
	bounds, err := spec.Bounds(at)
	if err != nil || !bounds.Start.Equal(time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)) || !bounds.End.Equal(time.Date(2026, 7, 9, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("bounds = %#v, err=%v", bounds, err)
	}
}

func TestRuleMatchingAndQuantityEvaluation(t *testing.T) {
	t.Parallel()
	rule := Rule{ID: "quota-a", Kind: RuleKindQuota, Mode: RuleModeStrict, Unit: AmountUnitRequests, Limit: Amount{Unit: AmountUnitRequests, Value: 10}, Match: DimensionsMatcher{Tenant: DimensionMatcher{Value: scope.Known("tenant-a")}}}
	ctx := EvaluationContext{Dimensions: Dimensions{Tenant: scope.Known("tenant-a")}, Amount: Amount{Unit: AmountUnitRequests, Value: 12}, At: time.Now().UTC()}
	got := EvaluateRules([]Rule{rule}, ctx)
	if got.Selected.Outcome != DecisionOutcomeDeny || !got.Selected.Exceeded {
		t.Fatalf("quantity evaluation = %#v, want deny/exceeded", got.Selected)
	}
	retired := Rule{ID: "legacy", Kind: RuleKind("spend_cap"), Unit: AmountUnit("money_nano"), Limit: Amount{Unit: AmountUnit("money_nano"), Value: 1}, Basis: BasisLegacyProviderPreferredAttempt}
	if err := retired.Validate(); !errors.Is(err, ErrRetiredMonetaryAuthority) {
		t.Fatalf("retired rule error = %v, want migration error", err)
	}
}

func TestReservationAndSettlementIdempotency(t *testing.T) {
	t.Parallel()
	reservationKey := ReservationKey{LogicalRequestID: "req-1", ALegID: "a-1", BLegID: "b-1", AttemptID: "attempt-1", RuleID: "quota", Sequence: 1}
	settlementKey := SettlementKey{ReservationKey: reservationKey, Sequence: 1}
	balance := WindowBalance{}
	first := balance.Settle(settlementKey, Amount{Unit: AmountUnitRequests, Value: 10}, Amount{Unit: AmountUnitRequests, Value: 7})
	if !first.Applied || balance.Consumed.Value != 7 || balance.Released.Value != 3 {
		t.Fatalf("first settlement = %#v, balance=%#v", first, balance)
	}
	if balance.Settle(settlementKey, Amount{Unit: AmountUnitRequests, Value: 10}, Amount{Unit: AmountUnitRequests, Value: 7}).Applied {
		t.Fatal("duplicate settlement applied")
	}
	if !balance.Release(ReleaseKey{ReservationKey: reservationKey, Sequence: 1}, Amount{Unit: AmountUnitRequests, Value: 4}).Applied {
		t.Fatal("release did not apply")
	}
}
