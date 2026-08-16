package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteReconcileBillingAccountSeparatesFinancialAndExposureProofs(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "combined-reconcile", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 25, Currency: "USD"}, PricingRef: billing.VersionRef{ID: "p", Version: "1"}, ChargePolicyRef: billing.VersionRef{ID: "c", Version: "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccountReconcileRequired(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	report, err := store.ReconcileBillingAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || !report.Financial.OK || !report.Exposure.OK || report.Exposure.Open.Nano != 25 {
		t.Fatalf("combined reconciliation = %+v", report)
	}
	ready, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != billing.AccountReady {
		t.Fatalf("verified reconciliation state = %q, want ready", ready.State)
	}
}

func TestSQLiteReconcileOpenExposureIsIndependentFromFinancialJournal(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "exposure-reconcile", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 25, Currency: "USD"}, PricingRef: billing.VersionRef{ID: "p", Version: "1"}, ChargePolicyRef: billing.VersionRef{ID: "c", Version: "1"}}); err != nil {
		t.Fatal(err)
	}
	report, err := store.ReconcileOpenExposure(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || report.Open.Nano != 25 || report.Rows != 1 {
		t.Fatalf("exposure reconciliation = %+v", report)
	}
	gotAccount, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotAccount.BalanceNano != 100 || gotAccount.ReservedNano != 0 {
		t.Fatalf("exposure reconciliation mutated financial state: %+v", gotAccount)
	}
}

func TestSQLiteRepairExposureNoChargeRequiresCompleteEvidence(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "exposure-repair", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID, ALegID: "a-repair", StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(), Outcome: billing.TurnOutcomeFailed, CustomerPricingRef: billing.VersionRef{ID: "p", Version: "1"}, ChargePolicyRef: billing.VersionRef{ID: "c", Version: "1"}}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 25, Currency: "USD"}, PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RepairExposureNoCharge(ctx, callID, "operator-repair-1"); !errors.Is(err, billing.ErrCallIncomplete) {
		t.Fatalf("repair without closure = %v, want ErrCallIncomplete", err)
	}
	if _, err := store.GetCallExposure(ctx, callID); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	settled, err := store.RepairExposureNoCharge(ctx, callID, "operator-repair-1")
	if err != nil {
		t.Fatal(err)
	}
	if settled.Replayed || settled.Customer.After.BalanceNano != account.BalanceNano {
		t.Fatalf("repair settlement = %+v", settled)
	}
	closed, err := store.GetCallExposure(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.IsOpen() || closed.Max.Nano != exposure.Max.Nano {
		t.Fatalf("repaired exposure = %+v", closed)
	}
	replayed, err := store.RepairExposureNoCharge(ctx, callID, "operator-repair-1")
	if err != nil || !replayed.Replayed {
		t.Fatalf("repeated repair = %+v, err=%v, want idempotent replay", replayed, err)
	}
}

func TestSQLiteRepairIncompleteCallNoChargeSynthesizesMissingLegs(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "incomplete-repair", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	pricing := billing.VersionRef{ID: "p", Version: "1"}
	policy := billing.VersionRef{ID: "c", Version: "1"}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 40, Currency: "USD"}, PricingRef: pricing, ChargePolicyRef: policy})
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID, ALegID: "a-incomplete",
		StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(), Outcome: billing.TurnOutcomeFailed,
		CustomerPricingRef: pricing, ChargePolicyRef: policy, ExpectedBLegIDs: []string{"b-ghost", "b-present"},
	}
	if _, err := store.RepairIncompleteCallNoCharge(ctx, callID, "incomplete-repair-1"); !errors.Is(err, billing.ErrCallIncomplete) {
		t.Fatalf("incomplete repair without closure = %v, want ErrCallIncomplete", err)
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	present := billing.CallLegUsageRecord{
		CallID: callID, ALegID: "a-incomplete", BLegID: "b-present", BackendID: "be", ProviderID: "be", ModelID: "m",
		StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(), Outcome: billing.LegOutcomeFailed, Surfaced: billing.SurfacedNo,
		Evidence: billing.FinalBillingEvidence{Source: billing.EvidenceSourceUnavailable, Authority: billing.EvidenceAuthorityUnavailable},
	}
	if err := store.AppendCallLegUsage(ctx, present); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RepairExposureNoCharge(ctx, callID, "complete-only"); !errors.Is(err, billing.ErrCallIncomplete) {
		t.Fatalf("complete repair with missing expected leg = %v, want ErrCallIncomplete", err)
	}
	settled, err := store.RepairIncompleteCallNoCharge(ctx, callID, "incomplete-repair-1")
	if err != nil {
		t.Fatal(err)
	}
	if settled.Replayed || settled.Customer.After.BalanceNano != account.BalanceNano {
		t.Fatalf("incomplete repair settlement = %+v", settled)
	}
	legs, err := store.ListCallLegUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]billing.LegOutcome{}
	for _, leg := range legs {
		seen[leg.BLegID] = leg.Outcome
	}
	if seen["b-ghost"] != billing.LegOutcomeNeverStarted {
		t.Fatalf("synthesized ghost leg outcome = %v, want never_started; legs=%#v", seen["b-ghost"], legs)
	}
	if seen["b-present"] != billing.LegOutcomeFailed {
		t.Fatalf("existing leg must remain unchanged: %#v", seen)
	}
	closed, err := store.GetCallExposure(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.IsOpen() || closed.Max.Nano != exposure.Max.Nano {
		t.Fatalf("repaired incomplete exposure = %+v", closed)
	}
	replayed, err := store.RepairIncompleteCallNoCharge(ctx, callID, "incomplete-repair-1")
	if err != nil || !replayed.Replayed {
		t.Fatalf("repeated incomplete repair = %+v, err=%v, want idempotent replay", replayed, err)
	}
	if _, err := store.RepairIncompleteCallNoCharge(ctx, callID, "incomplete-repair-2"); err == nil {
		t.Fatal("conflicting source key must fail closed after prior repair")
	}
}
