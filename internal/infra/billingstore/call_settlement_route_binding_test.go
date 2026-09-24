package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Task 14.1 BLOCKER 1 RED: terminal settlement compares the route tariff
// material actually used for each rated route against the admitted frozen
// binding and fails closed BEFORE any journal/balance/posting transition on
// version, content, route, or missing-binding mismatch.

func routeBindingFixture(route, id, version, hash string) billing.RouteTariffBinding {
	return billing.RouteTariffBinding{RouteID: route, TariffID: id, TariffVersion: version, ContentHash: hash}
}

func bindingHashFixture(seed byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, 64)
	for len(out) < 64 {
		out = append(out, hexdigits[seed%16], hexdigits[(seed^0x0f)%16])
		seed++
	}
	return string(out[:64])
}

//nolint:revive // test helper keeps t first per Go testing convention
func boundCallFixture(t *testing.T, store *DurableStore, ctx context.Context, accountID string, maxNano int64, bindings []billing.RouteTariffBinding) (billing.CallUsageRecord, billing.CallExposure) {
	t.Helper()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID, ALegID: "a-bound",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(), Outcome: billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v2"},
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(), Max: billing.Money{Nano: maxNano, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef, RouteTariffs: bindings,
	})
	if err != nil {
		t.Fatal(err)
	}
	return call, exposure
}

func boundResultFixture(call billing.CallUsageRecord, chargeNano int64, bindings []billing.RouteTariffBinding) billing.CallRatingResult {
	return billing.CallRatingResult{
		CallID: call.CallID, CustomerCharge: billing.Money{Nano: chargeNano, Currency: "USD"},
		Fingerprint: "bound-result", RouteTariffs: bindings,
	}
}

func TestSQLiteSettlementRejectsRouteTariffMismatchAtomically(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		used []billing.RouteTariffBinding
	}{
		{"version change", []billing.RouteTariffBinding{routeBindingFixture("a:m1", "model-pricing", "v2", bindingHashFixture('a'))}},
		{"content change", []billing.RouteTariffBinding{routeBindingFixture("a:m1", "model-pricing", "v1", bindingHashFixture('c'))}},
		{"route swap", []billing.RouteTariffBinding{routeBindingFixture("c:m3", "model-pricing", "v1", bindingHashFixture('a'))}},
		{"missing binding", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newSQLiteTestStore(t)
			ctx := context.Background()
			account := billing.Account{ID: "route-mismatch", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
			if err := store.CreateAccount(ctx, account); err != nil {
				t.Fatal(err)
			}
			admitted := []billing.RouteTariffBinding{routeBindingFixture("a:m1", "model-pricing", "v1", bindingHashFixture('a'))}
			call, exposure := boundCallFixture(t, store, ctx, account.ID, 60, admitted)
			_, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
				Call: call, Exposure: exposure, Result: boundResultFixture(call, 25, tc.used),
			})
			if !errors.Is(err, billing.ErrRatingSnapshotMismatch) {
				t.Fatalf("mismatch settle = %v, want ErrRatingSnapshotMismatch", err)
			}
			after, err := store.GetAccount(ctx, account.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.BalanceNano != 100 || after.State != billing.AccountReady || after.Version != 1 {
				t.Fatalf("mismatch mutated account: %+v", after)
			}
			var status string
			if err := store.db.NewRaw(`SELECT status FROM call_exposures WHERE call_id = ?`, call.CallID.String()).Scan(ctx, &status); err != nil {
				t.Fatal(err)
			}
			if status != "open" {
				t.Fatalf("mismatch exposure status = %q, want open", status)
			}
			transactions, err := store.JournalTransactions(ctx, account.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, transaction := range transactions {
				if transaction.OperationKind == "customer_call_settlement" {
					t.Fatalf("mismatch posted journal: %+v", transaction)
				}
			}
			var claimStatus string
			if err := store.db.NewRaw(`SELECT claim_status FROM usage_call_records WHERE call_id = ?`, call.CallID.String()).Scan(ctx, &claimStatus); err != nil {
				t.Fatal(err)
			}
			if claimStatus == "processed" {
				t.Fatal("mismatch advanced the call claim")
			}
		})
	}
}

func TestSQLiteSettlementRejectsUsedBindingsAgainstLegacyEmptyExposure(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "route-legacy-used", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	// Legacy scalar admission carries no bindings, but the result claims
	// route-specific tariff material: fail closed without any transition.
	call, exposure := boundCallFixture(t, store, ctx, account.ID, 60, nil)
	used := []billing.RouteTariffBinding{routeBindingFixture("a:m1", "model-pricing", "v1", bindingHashFixture('a'))}
	_, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: boundResultFixture(call, 25, used)})
	if !errors.Is(err, billing.ErrRatingSnapshotMismatch) {
		t.Fatalf("used-against-legacy settle = %v, want ErrRatingSnapshotMismatch", err)
	}
	after, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.BalanceNano != 100 || after.Version != 1 {
		t.Fatalf("rejected settlement mutated account: %+v", after)
	}
	open, err := store.GetCallExposure(ctx, call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if !open.IsOpen() {
		t.Fatal("rejected settlement must leave the exposure open")
	}
}

func TestSQLiteSettlementWithMatchingRouteTariffsSettlesAndReplays(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "route-match", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	admitted := []billing.RouteTariffBinding{
		routeBindingFixture("a:m1", "model-pricing", "v1", bindingHashFixture('a')),
		routeBindingFixture("b:m2", "retail-pricing", "v3", bindingHashFixture('b')),
	}
	call, exposure := boundCallFixture(t, store, ctx, account.ID, 60, admitted)
	// Settlement may rate a subset (failover winner only).
	result := boundResultFixture(call, 25, admitted[:1])
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result})
	if err != nil {
		t.Fatalf("matching settle = %v", err)
	}
	if settled.Replayed || settled.Breached {
		t.Fatalf("settlement = %+v, want fresh non-breach", settled)
	}
	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 75 {
		t.Fatalf("balance = %d, want 75", got.BalanceNano)
	}
	replayed, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result})
	if err != nil {
		t.Fatalf("matching replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatal("identical bound redelivery was not marked replayed")
	}
	// Same charge under a different route version must conflict, not rewrite.
	conflict := boundResultFixture(call, 25, []billing.RouteTariffBinding{routeBindingFixture("a:m1", "model-pricing", "v2", bindingHashFixture('a'))})
	conflict.Fingerprint = "bound-result-v2"
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: conflict}); !errors.Is(err, billing.ErrRatingSnapshotMismatch) {
		t.Fatalf("rebound mismatch = %v, want ErrRatingSnapshotMismatch", err)
	}
}

func TestSQLiteSettlementRouteBindingLegacyAndZeroChargeCompatible(t *testing.T) {
	t.Parallel()
	t.Run("legacy no bindings either side", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteTestStore(t)
		ctx := context.Background()
		account := billing.Account{ID: "route-legacy", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
		if err := store.CreateAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		call, exposure := boundCallFixture(t, store, ctx, account.ID, 60, nil)
		if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: boundResultFixture(call, 25, nil)}); err != nil {
			t.Fatalf("legacy settle = %v", err)
		}
	})
	t.Run("zero charge with omitted attestation rejects atomically", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteTestStore(t)
		ctx := context.Background()
		account := billing.Account{ID: "route-zero-omit", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
		if err := store.CreateAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		admitted := []billing.RouteTariffBinding{routeBindingFixture("a:m1", "model-pricing", "v1", bindingHashFixture('a'))}
		call, exposure := boundCallFixture(t, store, ctx, account.ID, 60, admitted)
		_, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: boundResultFixture(call, 0, nil)})
		if !errors.Is(err, billing.ErrRatingSnapshotMismatch) {
			t.Fatalf("omitted attestation at zero charge = %v, want ErrRatingSnapshotMismatch", err)
		}
		after, err := store.GetAccount(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.BalanceNano != 100 || after.Version != 1 {
			t.Fatalf("rejected zero settlement mutated account: %+v", after)
		}
		open, err := store.GetCallExposure(ctx, call.CallID)
		if err != nil {
			t.Fatal(err)
		}
		if !open.IsOpen() {
			t.Fatal("rejected zero settlement must leave the exposure open")
		}
	})
	t.Run("zero charge with exact attestation settles and replays", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteTestStore(t)
		ctx := context.Background()
		account := billing.Account{ID: "route-zero", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
		if err := store.CreateAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		admitted := []billing.RouteTariffBinding{routeBindingFixture("a:m1", "model-pricing", "v1", bindingHashFixture('a'))}
		call, exposure := boundCallFixture(t, store, ctx, account.ID, 60, admitted)
		settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: boundResultFixture(call, 0, admitted)})
		if err != nil {
			t.Fatalf("attested zero settlement = %v", err)
		}
		if settled.Breached {
			t.Fatalf("zero settlement = %+v, must not breach", settled)
		}
		closed, err := store.GetCallExposure(ctx, call.CallID)
		if err != nil {
			t.Fatal(err)
		}
		if closed.IsOpen() {
			t.Fatal("attested zero-charge exposure must close")
		}
		replayed, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: boundResultFixture(call, 0, admitted)})
		if err != nil || !replayed.Replayed {
			t.Fatalf("attested zero replay = %+v, err=%v, want idempotent replay", replayed, err)
		}
	})
}

//nolint:revive // test helper keeps t first per Go testing convention
func boundRepairFixture(t *testing.T, store *DurableStore, ctx context.Context, accountID, backendID, modelID string, bindings []billing.RouteTariffBinding) billing.BillingCallID {
	t.Helper()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID, ALegID: "a-repair-bound",
		StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(), Outcome: billing.TurnOutcomeFailed,
		CustomerPricingRef: billing.VersionRef{ID: "p", Version: "1"}, ChargePolicyRef: billing.VersionRef{ID: "c", Version: "1"},
		ExpectedBLegIDs: []string{"b-1"},
	}
	if _, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(), Max: billing.Money{Nano: 40, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef, RouteTariffs: bindings,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	leg := billing.CallLegUsageRecord{
		CallID: callID, ALegID: "a-repair-bound", BLegID: "b-1", BackendID: backendID, ProviderID: backendID, ModelID: modelID,
		StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(), Outcome: billing.LegOutcomeFailed, Surfaced: billing.SurfacedNo,
		Evidence: billing.FinalBillingEvidence{Source: billing.EvidenceSourceUnavailable, Authority: billing.EvidenceAuthorityUnavailable},
	}
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	return callID
}

func TestSQLiteRepairAttestsNoUsageRoutesAgainstAdmittedBindings(t *testing.T) {
	t.Parallel()
	admitted := []billing.RouteTariffBinding{routeBindingFixture("a:m1", "model-pricing", "v1", bindingHashFixture('a'))}
	t.Run("admitted leg route closes with attestation", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteTestStore(t)
		ctx := context.Background()
		account := billing.Account{ID: "repair-attested", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
		if err := store.CreateAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		callID := boundRepairFixture(t, store, ctx, account.ID, "a", "m1", admitted)
		settled, err := store.RepairExposureNoCharge(ctx, callID, "repair-attested-1")
		if err != nil {
			t.Fatalf("attested repair = %v", err)
		}
		if settled.Replayed {
			t.Fatal("first attested repair was marked replayed")
		}
		got, err := store.GetAccount(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.BalanceNano != 100 {
			t.Fatalf("attested repair mutated balance: %+v", got)
		}
		closed, err := store.GetCallExposure(ctx, callID)
		if err != nil {
			t.Fatal(err)
		}
		if closed.IsOpen() {
			t.Fatal("attested repair must close the exposure")
		}
		replayed, err := store.RepairExposureNoCharge(ctx, callID, "repair-attested-1")
		if err != nil || !replayed.Replayed {
			t.Fatalf("attested repair replay = %+v, err=%v, want idempotent", replayed, err)
		}
	})
	t.Run("unadmitted leg route fails closed", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteTestStore(t)
		ctx := context.Background()
		account := billing.Account{ID: "repair-unadmitted", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
		if err := store.CreateAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		callID := boundRepairFixture(t, store, ctx, account.ID, "c", "m3", admitted)
		_, err := store.RepairExposureNoCharge(ctx, callID, "repair-unadmitted-1")
		if !errors.Is(err, billing.ErrRatingSnapshotMismatch) {
			t.Fatalf("unadmitted repair = %v, want ErrRatingSnapshotMismatch", err)
		}
		open, err := store.GetCallExposure(ctx, callID)
		if err != nil {
			t.Fatal(err)
		}
		if !open.IsOpen() {
			t.Fatal("failed repair must leave the exposure open")
		}
		after, err := store.GetAccount(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.BalanceNano != 100 {
			t.Fatalf("failed repair mutated balance: %+v", after)
		}
	})
}
