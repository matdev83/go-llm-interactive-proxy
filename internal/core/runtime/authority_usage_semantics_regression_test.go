package runtime

import (
	"context"
	"testing"

	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestSettlementAuthorityIsIndependentPerTokenUnit(t *testing.T) {
	t.Parallel()
	rec := &recordingAuthorityService{admitResult: authorityapp.AdmissionResult{
		Reserved: true,
		Reservations: []authorityapp.AdmissionReservation{
			{ReservationID: "r-input", RuleID: "input", ReservedAmount: domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 8}},
			{ReservationID: "r-output", RuleID: "output", ReservedAmount: domain.Amount{Unit: domain.AmountUnitOutputTokens, Value: 6}},
		},
	}}
	state := attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(8), admissionResult: rec.admitResult}
	lifecycle := newAuthorityLifecycle(rec, nil, state, authorityCandidate())
	inputOnly := lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: 5, TotalTokens: 5,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	if !lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, inputOnly, false) {
		t.Fatal("initial settle was not applied")
	}
	initial := rec.lastSettle()
	if len(initial.Reservations) != 2 {
		t.Fatalf("settlement descriptors = %d, want 2", len(initial.Reservations))
	}
	if initial.Reservations[0].Authority != domain.AuthorityLevelAuthoritative {
		t.Fatalf("input authority = %q, want authoritative", initial.Reservations[0].Authority)
	}
	if initial.Reservations[1].Authority != domain.AuthorityLevelEstimated || initial.Reservations[1].FinalUsage.Value != 6 {
		t.Fatalf("missing output descriptor = %#v, want estimated fallback 6", initial.Reservations[1])
	}
	outputOnly := lipapi.Event{
		Kind: lipapi.EventUsageDelta, OutputTokens: 3, TotalTokens: 3,
		Accounting: inputOnly.Accounting,
	}
	if !lifecycle.ReconcileAuthoritative(context.Background(), outputOnly) {
		t.Fatal("output reconciliation was not applied")
	}
	reconciled := rec.lastSettle()
	if len(reconciled.Reservations) != 1 || reconciled.Reservations[0].Reservation.Unit != domain.AmountUnitOutputTokens {
		t.Fatalf("reconciled descriptors = %#v, want output only", reconciled.Reservations)
	}
}

func TestAuthorityUsageEventDropsClientVisibleScopeFromBillableMerge(t *testing.T) {
	t.Parallel()

	provider := lipapi.ScopedUsageDelta{
		InputTokens:  20,
		OutputTokens: 5,
		TotalTokens:  25,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneProviderBillable,
			Source:    lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	client := lipapi.ScopedUsageDelta{
		InputTokens:  3,
		OutputTokens: 40,
		TotalTokens:  43,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneClientVisible,
			Source:    lipapi.UsageSourceLocalTokenizer,
			Authority: lipapi.UsageAuthorityEstimated,
		},
	}
	got := authorityUsageEvent([]lipapi.Event{{
		Kind:          lipapi.EventUsageDelta,
		UsageScopes:   []lipapi.ScopedUsageDelta{client, provider},
		CostNanoUnits: 99,
		Currency:      "USD",
		CostSource:    string(lipapi.UsageSourceLocalEstimator),
	}})
	if got.Kind != lipapi.EventUsageDelta || len(got.UsageScopes) != 1 {
		t.Fatalf("authority merge = %#v, want one provider scope", got)
	}
	if got.UsageScopes[0].Accounting != provider.Accounting || got.InputTokens != provider.InputTokens || got.OutputTokens != provider.OutputTokens {
		t.Fatalf("authority merge selected wrong counters/scope: %#v", got)
	}
	if authorityForSettlement(authorityapp.SettlementKindFinal, got) != domain.AuthorityLevelAuthoritative {
		t.Fatalf("authority = %q, want authoritative", authorityForSettlement(authorityapp.SettlementKindFinal, got))
	}
	if got.CostNanoUnits != 0 || got.Currency != "" || got.CostSource != "" {
		t.Fatalf("ambiguous event-level cost leaked into provider merge: %#v", got)
	}
}

func TestMergeUsageEventsReturnsAbsentEventWhenNothingIsIncluded(t *testing.T) {
	t.Parallel()

	if got := mergeUsageEventsForClient(nil, false); got.Kind != "" {
		t.Fatalf("empty merge kind = %q, want absent event", got.Kind)
	}
	got := mergeUsageEventsForClient([]lipapi.Event{{Kind: lipapi.EventUsageDelta, UsageScopes: []lipapi.ScopedUsageDelta{{
		Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneProviderBillable},
	}}}}, true)
	if got.Kind != "" {
		t.Fatalf("all-skipped merge kind = %q, want absent event", got.Kind)
	}
}

func TestUnmarkedProviderUsageIsNotAuthoritative(t *testing.T) {
	t.Parallel()

	if authoritativeProviderAccounting(lipapi.UsageAccountingMetadata{
		Plane:     lipapi.UsagePlaneProviderBillable,
		Authority: lipapi.UsageAuthorityAuthoritative,
	}) {
		t.Fatal("provider usage without an explicit provider source must not be authoritative")
	}
}
