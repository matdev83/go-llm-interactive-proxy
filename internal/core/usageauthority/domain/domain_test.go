package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestAmountValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		amount  Amount
		wantErr string
	}{
		{
			name:   "request amount is valid",
			amount: Amount{Unit: AmountUnitRequests, Value: 7},
		},
		{
			name:   "token amount is valid",
			amount: Amount{Unit: AmountUnitInputTokens, Value: 11},
		},
		{
			name:   "money amount is valid",
			amount: Amount{Unit: AmountUnitMoneyNano, Value: 42, Currency: "usd"},
		},
		{
			name:    "negative values are rejected",
			amount:  Amount{Unit: AmountUnitRequests, Value: -1},
			wantErr: "negative",
		},
		{
			name:    "money requires currency",
			amount:  Amount{Unit: AmountUnitMoneyNano, Value: 1},
			wantErr: "currency",
		},
		{
			name:    "unknown unit is rejected",
			amount:  Amount{Unit: AmountUnit("mystery"), Value: 1},
			wantErr: "unsupported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.amount.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() error = nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestDimensionKeyDeterministicAndPresenceAware(t *testing.T) {
	t.Parallel()

	first := Dimensions{
		Principal:    scope.Known("principal-a"),
		Tenant:       scope.Known("tenant-a"),
		Backend:      scope.Known("backend-a"),
		Model:        scope.Known("model-a"),
		Route:        scope.Known("route-a"),
		PolicyLabels: map[string]scope.Value{"beta": scope.Known("2"), "alpha": scope.Known("1")},
		Organization: scope.Known("org-a"),
		Workspace:    scope.Known(""),
		Project:      scope.Unknown(),
		Department:   scope.Known("dept-a"),
		CostCenter:   scope.Unknown(),
	}
	second := Dimensions{
		Principal:    scope.Known("principal-a"),
		Tenant:       scope.Known("tenant-a"),
		Backend:      scope.Known("backend-a"),
		Model:        scope.Known("model-a"),
		Route:        scope.Known("route-a"),
		PolicyLabels: map[string]scope.Value{"alpha": scope.Known("1"), "beta": scope.Known("2")},
		Organization: scope.Known("org-a"),
		Workspace:    scope.Known(""),
		Project:      scope.Unknown(),
		Department:   scope.Known("dept-a"),
		CostCenter:   scope.Unknown(),
	}

	if got, want := first.Key(), second.Key(); got != want {
		t.Fatalf("Key() mismatch\nfirst:  %s\nsecond: %s", got, want)
	}

	knownEmpty := Dimensions{Principal: scope.Known("")}
	unknown := Dimensions{Principal: scope.Unknown()}
	if got, want := knownEmpty.Key(), unknown.Key(); got == want {
		t.Fatalf("known-empty and unknown keys should differ: %s", got)
	}
}

func TestFixedWindowBoundsAndKey(t *testing.T) {
	t.Parallel()

	spec := WindowSpec{
		Algorithm: WindowAlgorithmFixed,
		Size:      time.Hour,
		Anchor:    time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC),
	}
	at := time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC)

	bounds, err := spec.Bounds(at)
	if err != nil {
		t.Fatalf("Bounds() error = %v", err)
	}

	wantStart := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 7, 9, 11, 0, 0, 0, time.UTC)
	if !bounds.Start.Equal(wantStart) || !bounds.End.Equal(wantEnd) {
		t.Fatalf("bounds = %v-%v, want %v-%v", bounds.Start, bounds.End, wantStart, wantEnd)
	}

	key := spec.Key("rule-1", Dimensions{Principal: scope.Known("principal-a")}, at)
	if key.RuleID != "rule-1" {
		t.Fatalf("RuleID = %q", key.RuleID)
	}
	if !key.Start.Equal(wantStart) || !key.End.Equal(wantEnd) {
		t.Fatalf("window key bounds = %v-%v, want %v-%v", key.Start, key.End, wantStart, wantEnd)
	}
}

func TestUnsupportedWindowAlgorithmRejected(t *testing.T) {
	t.Parallel()

	spec := WindowSpec{Algorithm: WindowAlgorithm("sliding"), Size: time.Hour, Anchor: time.Unix(0, 0).UTC()}
	if _, err := spec.Bounds(time.Now().UTC()); err == nil {
		t.Fatal("Bounds() error = nil")
	}
}

func TestRuleMatchingRespectsSafeKnownAndUnknownDimensions(t *testing.T) {
	t.Parallel()

	exact := Rule{
		ID:     "quota-a",
		Kind:   RuleKindQuota,
		Mode:   RuleModeStrict,
		Unit:   AmountUnitRequests,
		Limit:  Amount{Unit: AmountUnitRequests, Value: 10},
		Window: WindowSpec{Algorithm: WindowAlgorithmFixed, Size: time.Hour, Anchor: time.Unix(0, 0).UTC()},
		Match: DimensionsMatcher{
			Tenant:  DimensionMatcher{Value: scope.Known("tenant-a")},
			Backend: DimensionMatcher{Value: scope.Known("backend-a")},
			Labels: map[string]MatchValue{
				"tier": {Value: scope.Known("gold")},
			},
		},
	}

	unknownMatch := Rule{
		ID:     "quota-unknown",
		Kind:   RuleKindQuota,
		Mode:   RuleModeStrict,
		Unit:   AmountUnitRequests,
		Limit:  Amount{Unit: AmountUnitRequests, Value: 10},
		Window: WindowSpec{Algorithm: WindowAlgorithmFixed, Size: time.Hour, Anchor: time.Unix(0, 0).UTC()},
		Match: DimensionsMatcher{
			Backend: DimensionMatcher{MatchUnknown: true},
		},
	}

	exactCtx := EvaluationContext{
		Dimensions: Dimensions{
			Tenant:  scope.Known("tenant-a"),
			Backend: scope.Known("backend-a"),
			PolicyLabels: map[string]scope.Value{
				"tier": scope.Known("gold"),
			},
		},
		Amount: Amount{Unit: AmountUnitRequests, Value: 12},
		Spend:  Amount{Unit: AmountUnitMoneyNano, Value: 9, Currency: "usd"},
		At:     time.Unix(3600, 0).UTC(),
	}

	exactMatches := EvaluateRules([]Rule{exact}, exactCtx)
	if len(exactMatches.Matches) != 1 {
		t.Fatalf("matched rules = %d, want 1", len(exactMatches.Matches))
	}
	if exactMatches.Matches[0].RuleID != "quota-a" {
		t.Fatalf("matched rule = %q, want quota-a", exactMatches.Matches[0].RuleID)
	}

	unknownMatches := EvaluateRules([]Rule{unknownMatch}, EvaluationContext{
		Dimensions: Dimensions{Backend: scope.Unknown()},
		Amount:     Amount{Unit: AmountUnitRequests, Value: 1},
		Spend:      Amount{Unit: AmountUnitMoneyNano, Value: 1, Currency: "usd"},
		At:         time.Unix(3600, 0).UTC(),
	})
	if len(unknownMatches.Matches) != 1 {
		t.Fatalf("matched rules = %d, want 1", len(unknownMatches.Matches))
	}
	if unknownMatches.Matches[0].RuleID != "quota-unknown" {
		t.Fatalf("matched rule = %q, want quota-unknown", unknownMatches.Matches[0].RuleID)
	}

	negative := Rule{
		ID:     "quota-known-empty",
		Kind:   RuleKindQuota,
		Mode:   RuleModeStrict,
		Unit:   AmountUnitRequests,
		Limit:  Amount{Unit: AmountUnitRequests, Value: 10},
		Window: WindowSpec{Algorithm: WindowAlgorithmFixed, Size: time.Hour, Anchor: time.Unix(0, 0).UTC()},
		Match: DimensionsMatcher{
			Tenant: DimensionMatcher{Value: scope.Known("")},
		},
	}
	if ok := negative.Matches(Dimensions{Tenant: scope.Unknown()}); ok {
		t.Fatal("known-empty matcher should not match unknown value")
	}
}

func TestEvaluateRulesSelectsMostRestrictiveExceededOutcome(t *testing.T) {
	t.Parallel()

	rules := []Rule{
		{
			ID:    "quota-strict",
			Kind:  RuleKindQuota,
			Mode:  RuleModeStrict,
			Unit:  AmountUnitRequests,
			Limit: Amount{Unit: AmountUnitRequests, Value: 10},
		},
		{
			ID:    "budget-advisory",
			Kind:  RuleKindBudget,
			Mode:  RuleModeAdvisory,
			Unit:  AmountUnitMoneyNano,
			Limit: Amount{Unit: AmountUnitMoneyNano, Value: 50, Currency: "usd"},
		},
		{
			ID:    "spend-cap-strict",
			Kind:  RuleKindSpendCap,
			Mode:  RuleModeStrict,
			Unit:  AmountUnitMoneyNano,
			Limit: Amount{Unit: AmountUnitMoneyNano, Value: 100, Currency: "usd"},
		},
	}

	got := EvaluateRules(rules, EvaluationContext{
		Dimensions: Dimensions{Tenant: scope.Known("tenant-a")},
		Amount:     Amount{Unit: AmountUnitRequests, Value: 12},
		Spend:      Amount{Unit: AmountUnitMoneyNano, Value: 120, Currency: "usd"},
		At:         time.Unix(3600, 0).UTC(),
	})

	if got.Selected.Outcome != DecisionOutcomeDeny {
		t.Fatalf("selected outcome = %q, want %q", got.Selected.Outcome, DecisionOutcomeDeny)
	}
	if got.Selected.RuleID != "quota-strict" {
		t.Fatalf("selected rule = %q, want quota-strict", got.Selected.RuleID)
	}
	if !got.Selected.Exceeded {
		t.Fatal("selected outcome should be exceeded")
	}
	if got.Selected.RuleIDs == nil || len(got.Selected.RuleIDs) != 3 {
		t.Fatalf("matched rule ids = %#v, want 3 entries", got.Selected.RuleIDs)
	}
}

func TestReservationAndSettlementIdempotency(t *testing.T) {
	t.Parallel()

	reservationKey := ReservationKey{
		LogicalRequestID: "req-1",
		ALegID:           "a-1",
		BLegID:           "b-1",
		AttemptID:        "attempt-1",
		RuleID:           "quota-strict",
		Sequence:         1,
	}
	settlementKey := SettlementKey{
		ReservationKey: reservationKey,
		Sequence:       1,
	}
	releaseKey := ReleaseKey{
		ReservationKey: reservationKey,
		Sequence:       1,
	}

	if got, want := reservationKey.String(), "req-1|a-1|b-1|attempt-1|quota-strict|1"; got != want {
		t.Fatalf("reservation key = %q, want %q", got, want)
	}
	if got, want := settlementKey.String(), reservationKey.String()+"|settle|1"; got != want {
		t.Fatalf("settlement key = %q, want %q", got, want)
	}
	if got, want := releaseKey.String(), reservationKey.String()+"|release|1"; got != want {
		t.Fatalf("release key = %q, want %q", got, want)
	}

	balance := WindowBalance{}
	first := balance.Settle(settlementKey, Amount{Unit: AmountUnitRequests, Value: 10}, Amount{Unit: AmountUnitRequests, Value: 7})
	if !first.Applied {
		t.Fatal("first settlement should apply")
	}
	if got, want := balance.Consumed.Value, int64(7); got != want {
		t.Fatalf("consumed = %d, want %d", got, want)
	}
	if got, want := balance.Released.Value, int64(3); got != want {
		t.Fatalf("released = %d, want %d", got, want)
	}
	if got, want := balance.Overage.Value, int64(0); got != want {
		t.Fatalf("overage = %d, want %d", got, want)
	}

	second := balance.Settle(settlementKey, Amount{Unit: AmountUnitRequests, Value: 10}, Amount{Unit: AmountUnitRequests, Value: 7})
	if second.Applied {
		t.Fatal("duplicate settlement should not apply")
	}
	if got, want := balance.Consumed.Value, int64(7); got != want {
		t.Fatalf("consumed after duplicate = %d, want %d", got, want)
	}
	if got, want := balance.Released.Value, int64(3); got != want {
		t.Fatalf("released after duplicate = %d, want %d", got, want)
	}

	overageKey := SettlementKey{
		ReservationKey: reservationKey,
		Sequence:       2,
	}
	overage := balance.Settle(overageKey, Amount{Unit: AmountUnitRequests, Value: 10}, Amount{Unit: AmountUnitRequests, Value: 12})
	if !overage.Applied {
		t.Fatal("overage settlement should apply")
	}
	if got, want := balance.Overage.Value, int64(2); got != want {
		t.Fatalf("overage after excess = %d, want %d", got, want)
	}
	_ = balance.Release(releaseKey, Amount{Unit: AmountUnitRequests, Value: 4})
	_ = balance.Release(releaseKey, Amount{Unit: AmountUnitRequests, Value: 4})
	if got, want := balance.Released.Value, int64(7); got != want {
		t.Fatalf("released after idempotent release = %d, want %d", got, want)
	}
}

func TestAuthorityStatusAndValidation(t *testing.T) {
	t.Parallel()

	if got := (AuthorityStatus{State: AuthorityStateReady, Reason: StatusReasonNone}); got.Validate() != nil {
		t.Fatalf("ready status should validate, got %v", got.Validate())
	}

	if err := (AuthorityStatus{State: AuthorityStateReady, Reason: StatusReason("secret")}).Validate(); err == nil {
		t.Fatal("unknown reason should be rejected")
	}

	strictRule := Rule{
		ID:    "quota-strict",
		Kind:  RuleKindQuota,
		Mode:  RuleModeStrict,
		Unit:  AmountUnitRequests,
		Limit: Amount{Unit: AmountUnitRequests, Value: 10},
		Basis: BasisLegacyProviderPreferredAttempt,
	}
	if err := strictRule.Validate(); err != nil {
		t.Fatalf("strict rule should validate: %v", err)
	}

	if err := (AuthorityConfig{
		Enabled: true,
		Backing: BackingCapabilityAdvisoryOnly,
		Rules:   []Rule{strictRule},
	}).Validate(); err == nil {
		t.Fatal("strict rule should be rejected on advisory-only backing")
	}

	status := (AuthorityConfig{Enabled: false, Backing: BackingCapabilityAtomic}).Status()
	if status.State != AuthorityStateDisabled {
		t.Fatalf("disabled status = %q, want disabled", status.State)
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("disabled status should validate: %v", err)
	}

	ready := (AuthorityConfig{Enabled: true, Backing: BackingCapabilityAtomic}).Status()
	if ready.State != AuthorityStateReady {
		t.Fatalf("ready status = %q, want ready", ready.State)
	}
	advisory := (AuthorityConfig{Enabled: true, Backing: BackingCapabilityAdvisoryOnly}).Status()
	if advisory.State != AuthorityStateAdvisoryOnly {
		t.Fatalf("advisory status = %q, want advisory_only", advisory.State)
	}
	unavailable := AuthorityStatus{State: AuthorityStateUnavailable, Reason: StatusReasonBackingUnavailable}
	if err := unavailable.Validate(); err != nil {
		t.Fatalf("unavailable status should validate: %v", err)
	}
	degraded := AuthorityStatus{State: AuthorityStateDegraded, Reason: StatusReasonBackingDegraded}
	if err := degraded.Validate(); err != nil {
		t.Fatalf("degraded status should validate: %v", err)
	}
}

func TestValidationRejectsUnsafeRuleDefinitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rule Rule
	}{
		{
			name: "empty id",
			rule: Rule{Kind: RuleKindQuota, Mode: RuleModeStrict, Unit: AmountUnitRequests, Limit: Amount{Unit: AmountUnitRequests, Value: 1}},
		},
		{
			name: "bad currency",
			rule: Rule{ID: "budget", Kind: RuleKindBudget, Mode: RuleModeStrict, Unit: AmountUnitMoneyNano, Limit: Amount{Unit: AmountUnitMoneyNano, Value: 1}},
		},
		{
			name: "bad window size",
			rule: Rule{ID: "rate", Kind: RuleKindRate, Mode: RuleModeStrict, Unit: AmountUnitRequests, Limit: Amount{Unit: AmountUnitRequests, Value: 1}, Window: WindowSpec{Algorithm: WindowAlgorithmFixed, Size: 0, Anchor: time.Unix(0, 0)}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := tt.rule.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestStatusFromBackingClassifiesKnownPostures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		backing BackingCapability
		want    AuthorityState
	}{
		{backing: BackingCapabilityDisabled, want: AuthorityStateDisabled},
		{backing: BackingCapabilityAtomic, want: AuthorityStateReady},
		{backing: BackingCapabilityAdvisoryOnly, want: AuthorityStateAdvisoryOnly},
		{backing: BackingCapabilityUnavailable, want: AuthorityStateUnavailable},
		{backing: BackingCapabilityDegraded, want: AuthorityStateDegraded},
	}

	for _, tt := range cases {
		t.Run(string(tt.backing), func(t *testing.T) {
			t.Parallel()
			got := StatusFromBacking(tt.backing)
			if got.State != tt.want {
				t.Fatalf("State = %q, want %q", got.State, tt.want)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestDimensionKeyRejectsInvalidPolicyLabelKeys(t *testing.T) {
	t.Parallel()

	_, err := (Dimensions{
		PolicyLabels: map[string]scope.Value{"bad key": scope.Known("value")},
	}).KeyErr()
	if err == nil {
		t.Fatal("KeyErr() error = nil")
	}
}

// TestDimensionKeyIncludesCredential pins requirement 1.2: the credential
// authority dimension must participate in the dimension key so distinct
// credentials produce distinct accounting windows, while preserving the
// known-vs-unknown presence semantics used by every other scope dimension.
func TestDimensionKeyIncludesCredential(t *testing.T) {
	t.Parallel()

	withCredential := Dimensions{
		Principal:  scope.Known("principal-a"),
		Credential: scope.Known("cred-a"),
	}
	withoutCredential := Dimensions{
		Principal:  scope.Known("principal-a"),
		Credential: scope.Unknown(),
	}
	if withCredential.Key() == withoutCredential.Key() {
		t.Fatalf("credential must distinguish keys:\nwith:    %s\nwithout: %s", withCredential.Key(), withoutCredential.Key())
	}

	knownEmpty := Dimensions{Credential: scope.Known("")}
	unknown := Dimensions{Credential: scope.Unknown()}
	if knownEmpty.Key() == unknown.Key() {
		t.Fatalf("known-empty and unknown credential keys should differ: %s", knownEmpty.Key())
	}

	sameA := Dimensions{Credential: scope.Known("cred-a")}
	sameB := Dimensions{Credential: scope.Known("cred-a")}
	if sameA.Key() != sameB.Key() {
		t.Fatalf("equal credentials produced different keys:\n%s\n%s", sameA.Key(), sameB.Key())
	}
}

// TestDimensionsMatcherCredentialMatches pins the credential matcher
// behavior and the backward-compat wildcard: an unconfigured (zero)
// Credential matcher must match any credential exactly as before, while a
// configured matcher follows the same known/unknown/match-unknown rules as
// every other dimension.
func TestDimensionsMatcherCredentialMatches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		matcher DimensionMatcher
		actual  scope.Value
		want    bool
	}{
		{name: "unconfigured matcher matches known", matcher: DimensionMatcher{}, actual: scope.Known("cred-a"), want: true},
		{name: "unconfigured matcher matches unknown", matcher: DimensionMatcher{}, actual: scope.Unknown(), want: true},
		{name: "known value matches equal", matcher: DimensionMatcher{Value: scope.Known("cred-a")}, actual: scope.Known("cred-a"), want: true},
		{name: "known value rejects different", matcher: DimensionMatcher{Value: scope.Known("cred-a")}, actual: scope.Known("cred-b"), want: false},
		{name: "known value rejects unknown", matcher: DimensionMatcher{Value: scope.Known("cred-a")}, actual: scope.Unknown(), want: false},
		{name: "match-unknown matches unknown", matcher: DimensionMatcher{MatchUnknown: true}, actual: scope.Unknown(), want: true},
		{name: "match-unknown rejects known", matcher: DimensionMatcher{MatchUnknown: true}, actual: scope.Known("cred-a"), want: false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := DimensionsMatcher{Credential: tt.matcher}
			if got := m.Matches(Dimensions{Credential: tt.actual}); got != tt.want {
				t.Fatalf("Matches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSettlementKeyRejectsDuplicateEmptyInputs(t *testing.T) {
	t.Parallel()

	key := SettlementKey{}
	if got := key.String(); got == "" {
		t.Fatal("String() should still be deterministic for zero value")
	}
}

func TestWindowKeyStringFormat(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 7, 18, 12, 0, 0, 123456789, time.UTC)
	end := start.Add(time.Hour)
	key := WindowKey{
		RuleID:       "quota-strict",
		DimensionKey: DimensionKey("principal=p1"),
		Start:        start,
		End:          end,
	}
	want := "quota-strict|principal=p1|" + start.Format(time.RFC3339Nano) + "|" + end.Format(time.RFC3339Nano)
	if got := key.String(); got != want {
		t.Fatalf("window key = %q, want %q", got, want)
	}
}

func TestReservationKeyStringNamespace(t *testing.T) {
	t.Parallel()

	key := ReservationKey{
		LogicalRequestID: "req-1",
		RuleID:           "quota-strict",
		Sequence:         2,
		Namespace:        " " + NamespaceDefault + " ",
	}
	want := NamespaceDefault + "|req-1||||quota-strict|2"
	if got := key.String(); got != want {
		t.Fatalf("namespaced reservation key = %q, want %q", got, want)
	}
}

func TestRuleValidationRejectsUnsupportedAlgorithm(t *testing.T) {
	t.Parallel()

	rule := Rule{
		ID:    "quota",
		Kind:  RuleKindQuota,
		Mode:  RuleModeStrict,
		Unit:  AmountUnitRequests,
		Limit: Amount{Unit: AmountUnitRequests, Value: 1},
		Window: WindowSpec{
			Algorithm: WindowAlgorithm("sliding"),
			Size:      time.Hour,
			Anchor:    time.Unix(0, 0),
		},
	}
	if err := rule.Validate(); err == nil {
		t.Fatal("unsupported algorithm should be rejected")
	}
}

func TestRuleValidationEnforcesKindUnitAndCurrency(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rule      Rule
		want      string
		wantErrIs error
	}{
		{
			name: "quota cannot use money",
			rule: Rule{
				ID:       "quota-money",
				Kind:     RuleKindQuota,
				Mode:     RuleModeStrict,
				Unit:     AmountUnitMoneyNano,
				Limit:    Amount{Unit: AmountUnitMoneyNano, Value: 1, Currency: "usd"},
				Currency: "usd",
			},
			want: "quota and rate rules must not use money",
		},
		{
			name: "rate cannot use money",
			rule: Rule{
				ID:       "rate-money",
				Kind:     RuleKindRate,
				Mode:     RuleModeStrict,
				Unit:     AmountUnitMoneyNano,
				Limit:    Amount{Unit: AmountUnitMoneyNano, Value: 1, Currency: "usd"},
				Currency: "usd",
			},
			want: "quota and rate rules must not use money",
		},
		{
			name: "budget requires money",
			rule: Rule{
				ID:    "budget-tokens",
				Kind:  RuleKindBudget,
				Mode:  RuleModeStrict,
				Unit:  AmountUnitRequests,
				Limit: Amount{Unit: AmountUnitRequests, Value: 1},
			},
			want: "budget and spend-cap rules must use money",
		},
		{
			name: "spend cap requires money",
			rule: Rule{
				ID:    "spend-cap-tokens",
				Kind:  RuleKindSpendCap,
				Mode:  RuleModeStrict,
				Unit:  AmountUnitRequests,
				Limit: Amount{Unit: AmountUnitRequests, Value: 1},
			},
			want: "budget and spend-cap rules must use money",
		},
		{
			name: "unsupported rule unit is surfaced",
			rule: Rule{
				ID:    "quota-unsupported",
				Kind:  RuleKindQuota,
				Mode:  RuleModeStrict,
				Unit:  AmountUnit("mystery"),
				Limit: Amount{Unit: AmountUnitRequests, Value: 1},
			},
			wantErrIs: ErrUnsupportedRuleUnit,
		},
		{
			name: "budget currency mismatch is rejected",
			rule: Rule{
				ID:       "budget-currency",
				Kind:     RuleKindBudget,
				Mode:     RuleModeStrict,
				Unit:     AmountUnitMoneyNano,
				Limit:    Amount{Unit: AmountUnitMoneyNano, Value: 1, Currency: "eur"},
				Currency: "usd",
			},
			want: "currency mismatch",
		},
		{
			name: "rule unit must match limit unit",
			rule: Rule{
				ID:    "quota-unit-mismatch",
				Kind:  RuleKindQuota,
				Mode:  RuleModeStrict,
				Unit:  AmountUnitRequests,
				Limit: Amount{Unit: AmountUnitInputTokens, Value: 1},
			},
			want: "does not match limit unit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.rule.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil")
			}
			if tt.wantErrIs != nil {
				if !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("Validate() error = %v, want errors.Is(..., %v)", err, tt.wantErrIs)
				}
				return
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestEvaluateRulesAdvisoryAuthorityUnavailableNeverBlocksStrictAllow(t *testing.T) {
	t.Parallel()

	advisory := Rule{
		ID:                   "advisory-authoritative",
		Kind:                 RuleKindQuota,
		Mode:                 RuleModeAdvisory,
		Unit:                 AmountUnitRequests,
		Limit:                Amount{Unit: AmountUnitRequests, Value: 10},
		AuthorityRequirement: AuthorityRequirementAuthoritative,
	}
	strict := Rule{
		ID:    "strict-allow",
		Kind:  RuleKindQuota,
		Mode:  RuleModeStrict,
		Unit:  AmountUnitRequests,
		Limit: Amount{Unit: AmountUnitRequests, Value: 10},
	}
	got := EvaluateRules([]Rule{advisory, strict}, EvaluationContext{
		Amount:       Amount{Unit: AmountUnitRequests, Value: 1},
		RequestCount: Amount{Unit: AmountUnitRequests, Value: 1},
		Authority:    AuthorityLevelEstimated,
	})
	if len(got.Matches) != 2 || got.Matches[0].Outcome != DecisionOutcomeUnavailable {
		t.Fatalf("matches = %#v, want advisory unavailable match retained", got.Matches)
	}
	if got.Selected.Outcome != DecisionOutcomeAllow || len(got.Selected.RuleIDs) != 2 {
		t.Fatalf("selected outcome = %#v, want strict allow", got.Selected)
	}
}

func TestAuthorityStatusRejectsUnknownState(t *testing.T) {
	t.Parallel()

	if err := (AuthorityStatus{State: AuthorityState("mystery"), Reason: StatusReasonNone}).Validate(); err == nil {
		t.Fatal("unknown state should be rejected")
	}
}

func TestAmountStringerIsStableForMoney(t *testing.T) {
	t.Parallel()

	got := Amount{Unit: AmountUnitMoneyNano, Value: 123, Currency: "usd"}.String()
	if !strings.Contains(got, "usd") || !strings.Contains(got, "123") {
		t.Fatalf("String() = %q, want money details", got)
	}
}

func TestEvaluationIgnoresNonMatchingRule(t *testing.T) {
	t.Parallel()

	rule := Rule{
		ID:    "quota",
		Kind:  RuleKindQuota,
		Mode:  RuleModeStrict,
		Unit:  AmountUnitRequests,
		Limit: Amount{Unit: AmountUnitRequests, Value: 10},
		Match: DimensionsMatcher{Tenant: DimensionMatcher{Value: scope.Known("tenant-a")}},
	}

	got := EvaluateRules([]Rule{rule}, EvaluationContext{
		Dimensions: Dimensions{Tenant: scope.Known("tenant-b")},
		Amount:     Amount{Unit: AmountUnitRequests, Value: 1},
		Spend:      Amount{Unit: AmountUnitMoneyNano, Value: 1, Currency: "usd"},
		At:         time.Unix(3600, 0).UTC(),
	})
	if len(got.Matches) != 0 {
		t.Fatalf("matched rules = %#v, want none", got.Matches)
	}
	if got.Selected.Outcome != DecisionOutcomeAllow {
		t.Fatalf("selected outcome = %q, want allow", got.Selected.Outcome)
	}
}

// TestAuthoritativeOnlyRuleEstimatedEvidenceMatchesUnavailable pins finding 5
// and requirement 8.3: an authoritative-only rule evaluated against estimated
// evidence must still MATCH (appear in Matches and matched RuleIDs) and report
// an authority-unavailable outcome, rather than being silently dropped from
// evidence. The app resolves the unavailable posture via configured failure
// behavior; the domain only reports the outcome.
func TestAuthoritativeOnlyRuleEstimatedEvidenceMatchesUnavailable(t *testing.T) {
	t.Parallel()

	rule := Rule{
		ID:                   "budget-authoritative",
		Kind:                 RuleKindBudget,
		Mode:                 RuleModeStrict,
		Unit:                 AmountUnitMoneyNano,
		Currency:             "usd",
		Limit:                Amount{Unit: AmountUnitMoneyNano, Value: 100, Currency: "usd"},
		AuthorityRequirement: AuthorityRequirementAuthoritative,
	}
	ctx := EvaluationContext{
		Dimensions: Dimensions{Tenant: scope.Known("tenant-a")},
		Spend:      Amount{Unit: AmountUnitMoneyNano, Value: 50, Currency: "usd"},
		Authority:  AuthorityLevelEstimated,
		At:         time.Unix(3600, 0).UTC(),
	}

	got := EvaluateRules([]Rule{rule}, ctx)
	if len(got.Matches) != 1 {
		t.Fatalf("matched rules = %d, want 1 (authoritative-only rule must match on estimated evidence)", len(got.Matches))
	}
	if got.Matches[0].RuleID != "budget-authoritative" {
		t.Fatalf("matched rule = %q, want budget-authoritative", got.Matches[0].RuleID)
	}
	if got.Matches[0].Outcome != DecisionOutcomeUnavailable {
		t.Fatalf("outcome = %q, want unavailable", got.Matches[0].Outcome)
	}
	if got.Selected.Outcome != DecisionOutcomeUnavailable {
		t.Fatalf("selected outcome = %q, want unavailable", got.Selected.Outcome)
	}
	if got.Selected.RuleID != "budget-authoritative" {
		t.Fatalf("selected rule = %q, want budget-authoritative", got.Selected.RuleID)
	}
	if len(got.Selected.RuleIDs) != 1 || got.Selected.RuleIDs[0] != "budget-authoritative" {
		t.Fatalf("selected RuleIDs = %#v, want [budget-authoritative]", got.Selected.RuleIDs)
	}
}

// TestAuthoritativeOnlyRuleAuthoritativeEvidenceEvaluatesNormally pins the
// counterpart: when authoritative evidence is available, an authoritative-only
// rule evaluates against the limit normally (not unavailable).
func TestAuthoritativeOnlyRuleAuthoritativeEvidenceEvaluatesNormally(t *testing.T) {
	t.Parallel()

	rule := Rule{
		ID:                   "budget-authoritative",
		Kind:                 RuleKindBudget,
		Mode:                 RuleModeStrict,
		Unit:                 AmountUnitMoneyNano,
		Currency:             "usd",
		Limit:                Amount{Unit: AmountUnitMoneyNano, Value: 100, Currency: "usd"},
		AuthorityRequirement: AuthorityRequirementAuthoritative,
	}
	ctx := EvaluationContext{
		Dimensions: Dimensions{Tenant: scope.Known("tenant-a")},
		Spend:      Amount{Unit: AmountUnitMoneyNano, Value: 50, Currency: "usd"},
		Authority:  AuthorityLevelAuthoritative,
		At:         time.Unix(3600, 0).UTC(),
	}

	got := EvaluateRules([]Rule{rule}, ctx)
	if len(got.Matches) != 1 {
		t.Fatalf("matched rules = %d, want 1", len(got.Matches))
	}
	if got.Matches[0].Outcome != DecisionOutcomeAllow {
		t.Fatalf("outcome = %q, want allow (within budget under authoritative evidence)", got.Matches[0].Outcome)
	}
}

// TestAuthoritativeUnavailableDoesNotOverrideDeny pins the severity ordering
// (deny > clamp > advisory > unavailable > allow): when an authoritative-only
// rule reports unavailable alongside a deny from another matched rule, the
// deny wins selection while the unavailable match remains visible in Matches.
func TestAuthoritativeUnavailableDoesNotOverrideDeny(t *testing.T) {
	t.Parallel()

	rules := []Rule{
		{
			ID:                   "budget-authoritative",
			Kind:                 RuleKindBudget,
			Mode:                 RuleModeStrict,
			Unit:                 AmountUnitMoneyNano,
			Currency:             "usd",
			Limit:                Amount{Unit: AmountUnitMoneyNano, Value: 100, Currency: "usd"},
			AuthorityRequirement: AuthorityRequirementAuthoritative,
		},
		{
			ID:    "quota-strict",
			Kind:  RuleKindQuota,
			Mode:  RuleModeStrict,
			Unit:  AmountUnitRequests,
			Limit: Amount{Unit: AmountUnitRequests, Value: 10},
		},
	}
	ctx := EvaluationContext{
		Dimensions:   Dimensions{Tenant: scope.Known("tenant-a")},
		Amount:       Amount{Unit: AmountUnitRequests, Value: 12},
		RequestCount: Amount{Unit: AmountUnitRequests, Value: 12},
		Spend:        Amount{Unit: AmountUnitMoneyNano, Value: 50, Currency: "usd"},
		Authority:    AuthorityLevelEstimated,
		At:           time.Unix(3600, 0).UTC(),
	}

	got := EvaluateRules(rules, ctx)
	if got.Selected.Outcome != DecisionOutcomeDeny {
		t.Fatalf("selected outcome = %q, want deny (must override unavailable)", got.Selected.Outcome)
	}
	if got.Selected.RuleID != "quota-strict" {
		t.Fatalf("selected rule = %q, want quota-strict", got.Selected.RuleID)
	}
	// Both rules matched: the authoritative-only rule stays visible in Matches.
	var sawUnavailable bool
	for _, m := range got.Matches {
		if m.RuleID == "budget-authoritative" && m.Outcome == DecisionOutcomeUnavailable {
			sawUnavailable = true
		}
	}
	if !sawUnavailable {
		t.Fatalf("authoritative-only rule unavailable match must remain visible in Matches: %#v", got.Matches)
	}
	if len(got.Selected.RuleIDs) != 2 {
		t.Fatalf("matched RuleIDs = %#v, want 2 entries", got.Selected.RuleIDs)
	}
}

// TestSpendCapClampPopulatesRequestedMax pins finding 10 and requirement 6.5:
// a strict spend-cap that the request would exceed must produce a clamp
// outcome with RequestedMax populated from the requested spend basis and
// EffectiveMax left zero until the app fills it from live store remaining +
// cost estimate.
func TestSpendCapClampPopulatesRequestedMax(t *testing.T) {
	t.Parallel()

	rule := Rule{
		ID:       "spend-cap-strict",
		Kind:     RuleKindSpendCap,
		Mode:     RuleModeStrict,
		Unit:     AmountUnitMoneyNano,
		Currency: "usd",
		Limit:    Amount{Unit: AmountUnitMoneyNano, Value: 100, Currency: "usd"},
	}
	ctx := EvaluationContext{
		Dimensions: Dimensions{Tenant: scope.Known("tenant-a")},
		Spend:      Amount{Unit: AmountUnitMoneyNano, Value: 120, Currency: "usd"},
		Authority:  AuthorityLevelEstimated,
		At:         time.Unix(3600, 0).UTC(),
	}

	got := EvaluateRules([]Rule{rule}, ctx)
	if got.Selected.Outcome != DecisionOutcomeClamp {
		t.Fatalf("selected outcome = %q, want clamp", got.Selected.Outcome)
	}
	if !got.Selected.Exceeded {
		t.Fatal("selected outcome should be exceeded")
	}
	if got.Selected.RequestedMax.Unit != AmountUnitMoneyNano || got.Selected.RequestedMax.Value != 120 || got.Selected.RequestedMax.Currency != "usd" {
		t.Fatalf("RequestedMax = %#v, want 120 nano-usd (requested spend basis)", got.Selected.RequestedMax)
	}
	if got.Selected.EffectiveMax.Value != 0 {
		t.Fatalf("EffectiveMax = %d, want 0 (app fills from live store remaining)", got.Selected.EffectiveMax.Value)
	}
}

func TestMonetaryEvaluationRequiresSpendCurrency(t *testing.T) {
	t.Parallel()

	rule := Rule{
		ID:       "budget-usd",
		Kind:     RuleKindBudget,
		Mode:     RuleModeStrict,
		Unit:     AmountUnitMoneyNano,
		Currency: "usd",
		Limit:    Amount{Unit: AmountUnitMoneyNano, Value: 10, Currency: "usd"},
	}

	missingCurrency := EvaluateRules([]Rule{rule}, EvaluationContext{
		Dimensions: Dimensions{Tenant: scope.Known("tenant-a")},
		Spend:      Amount{Unit: AmountUnitMoneyNano, Value: 5},
		At:         time.Unix(3600, 0).UTC(),
	})
	if missingCurrency.Selected.Outcome != DecisionOutcomeUnavailable {
		t.Fatalf("missing spend currency outcome = %q, want unavailable", missingCurrency.Selected.Outcome)
	}

	mismatchedCurrency := EvaluateRules([]Rule{rule}, EvaluationContext{
		Dimensions: Dimensions{Tenant: scope.Known("tenant-a")},
		Spend:      Amount{Unit: AmountUnitMoneyNano, Value: 5, Currency: "eur"},
		At:         time.Unix(3600, 0).UTC(),
	})
	if mismatchedCurrency.Selected.Outcome != DecisionOutcomeUnavailable {
		t.Fatalf("mismatched spend currency outcome = %q, want unavailable", mismatchedCurrency.Selected.Outcome)
	}
}

func TestEvaluationUsesLimitUnitWhenRuleUnitMissing(t *testing.T) {
	t.Parallel()

	rule := Rule{
		ID:    "quota-requests",
		Kind:  RuleKindQuota,
		Mode:  RuleModeStrict,
		Limit: Amount{Unit: AmountUnitRequests, Value: 10},
	}

	got := EvaluateRules([]Rule{rule}, EvaluationContext{
		Dimensions: Dimensions{Tenant: scope.Known("tenant-a")},
		Amount:     Amount{Unit: AmountUnitInputTokens, Value: 1},
		At:         time.Unix(3600, 0).UTC(),
	})
	if got.Selected.Outcome != DecisionOutcomeUnavailable {
		t.Fatalf("selected outcome = %q, want unavailable", got.Selected.Outcome)
	}
}

func TestEvaluationUsesRequestCountForRequestUnitRules(t *testing.T) {
	t.Parallel()

	rule := Rule{
		ID:    "quota-requests",
		Kind:  RuleKindQuota,
		Mode:  RuleModeStrict,
		Unit:  AmountUnitRequests,
		Limit: Amount{Unit: AmountUnitRequests, Value: 10},
	}

	got := EvaluateRules([]Rule{rule}, EvaluationContext{
		Dimensions:   Dimensions{Tenant: scope.Known("tenant-a")},
		Amount:       Amount{Unit: AmountUnitInputTokens, Value: 500},
		RequestCount: Amount{Unit: AmountUnitRequests, Value: 1},
		At:           time.Unix(3600, 0).UTC(),
	})
	if got.Selected.Outcome != DecisionOutcomeAllow {
		t.Fatalf("selected outcome = %q, want allow", got.Selected.Outcome)
	}
	if got.Selected.Exceeded {
		t.Fatal("selected outcome should not be exceeded")
	}

	denied := EvaluateRules([]Rule{rule}, EvaluationContext{
		Dimensions:   Dimensions{Tenant: scope.Known("tenant-a")},
		Amount:       Amount{Unit: AmountUnitInputTokens, Value: 500},
		RequestCount: Amount{Unit: AmountUnitRequests, Value: 11},
		At:           time.Unix(3600, 0).UTC(),
	})
	if denied.Selected.Outcome != DecisionOutcomeDeny {
		t.Fatalf("selected outcome = %q, want deny", denied.Selected.Outcome)
	}
	if !denied.Selected.Exceeded {
		t.Fatal("selected outcome should be exceeded")
	}
}

func TestEvaluationUsesPreflightUsageForNonInputTokenRules(t *testing.T) {
	t.Parallel()

	rule := Rule{
		ID:    "quota-output",
		Kind:  RuleKindQuota,
		Mode:  RuleModeStrict,
		Unit:  AmountUnitOutputTokens,
		Limit: Amount{Unit: AmountUnitOutputTokens, Value: 10},
	}

	allowed := EvaluateRules([]Rule{rule}, EvaluationContext{
		Dimensions:     Dimensions{Tenant: scope.Known("tenant-a")},
		Amount:         Amount{Unit: AmountUnitInputTokens, Value: 500},
		PreflightUsage: PreflightUsage{InputTokens: 500, OutputTokens: 7},
		At:             time.Unix(3600, 0).UTC(),
	})
	if allowed.Selected.Outcome != DecisionOutcomeAllow {
		t.Fatalf("selected outcome = %q, want allow", allowed.Selected.Outcome)
	}

	denied := EvaluateRules([]Rule{rule}, EvaluationContext{
		Dimensions:     Dimensions{Tenant: scope.Known("tenant-a")},
		Amount:         Amount{Unit: AmountUnitInputTokens, Value: 500},
		PreflightUsage: PreflightUsage{InputTokens: 500, OutputTokens: 11},
		At:             time.Unix(3600, 0).UTC(),
	})
	if denied.Selected.Outcome != DecisionOutcomeDeny {
		t.Fatalf("selected outcome = %q, want deny", denied.Selected.Outcome)
	}
}

func TestStatusValidationRejectsUnknownReasonForReadyState(t *testing.T) {
	t.Parallel()

	err := AuthorityStatus{State: AuthorityStateReady, Reason: StatusReason("raw-secret")}.Validate()
	if err == nil {
		t.Fatal("unknown ready reason should be rejected")
	}
	if !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("Validate() error should wrap ErrInvalidStatus, got %v", err)
	}
}
