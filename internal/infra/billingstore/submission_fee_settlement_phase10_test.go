package billingstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestSQLiteSubmissionFeeSettlementIsClaimedOnceConcurrently(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "submission-concurrent", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}

	type pending struct {
		call     billing.CallUsageRecord
		exposure billing.CallExposure
		result   billing.CallRatingResult
	}
	pendingCalls := make([]pending, 2)
	for i := range pendingCalls {
		call, exposure := phase10SubmissionCall(t, store, account.ID, "submission-concurrent", fmt.Sprintf("a-concurrent-%d", i))
		pendingCalls[i] = pending{
			call:     call,
			exposure: exposure,
			result: billing.CallRatingResult{
				CallID: call.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"},
				Fingerprint:       fmt.Sprintf("submission-concurrent-result-%d", i),
				CustomerValuation: phase10SubmissionValuation(t, call, 5, 10, "tariff-v1", "policy-v1"),
			},
		}
	}

	errs := make(chan error, len(pendingCalls))
	var group sync.WaitGroup
	for _, item := range pendingCalls {
		item := item
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: item.call, Exposure: item.exposure, Result: item.result})
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent settlement: %v", err)
		}
	}

	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 975 {
		t.Fatalf("balance after two calls = %d, want 975 (15 + 10; submission fee once)", got.BalanceNano)
	}
	var claims int
	if err := store.db.NewRaw(`SELECT COUNT(*) FROM billing_submission_fee_claims WHERE store_id = ? AND account_id = ? AND submission_id = ?`, "test", account.ID, "submission-concurrent").Scan(ctx, &claims); err != nil {
		t.Fatal(err)
	}
	if claims != 1 {
		t.Fatalf("submission fee claims = %d, want one", claims)
	}
}

func TestSQLiteSubmissionFeeSettlementReplayAndScopes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteTestStore(t)
	account := billing.Account{ID: "submission-scopes-a", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	otherAccount := billing.Account{ID: "submission-scopes-b", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	for _, item := range []billing.Account{account, otherAccount} {
		if err := store.CreateAccount(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	firstCall, firstExposure := phase10SubmissionCall(t, store, account.ID, "submission-replay", "a-replay-1")
	firstResult := billing.CallRatingResult{CallID: firstCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-replay-1", CustomerValuation: phase10SubmissionValuation(t, firstCall, 5, 10, "tariff-v1", "policy-v1")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: firstCall, Exposure: firstExposure, Result: firstResult}); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: firstCall, Exposure: firstExposure, Result: firstResult})
	if err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("replay result = %+v, want Replayed", replayed)
	}
	conflictingReplay := firstResult
	conflictingReplay.CustomerValuation = phase10SubmissionValuation(t, firstCall, 5, 10, "tariff-v2", "policy-v1")
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: firstCall, Exposure: firstExposure, Result: conflictingReplay}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("same-call tariff conflict = %v, want ErrOperationConflict", err)
	}
	conflictingReplay.CustomerValuation = phase10SubmissionValuation(t, firstCall, 5, 10, "tariff-v1", "policy-v2")
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: firstCall, Exposure: firstExposure, Result: conflictingReplay}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("same-call policy conflict = %v, want ErrOperationConflict", err)
	}

	secondCall, secondExposure := phase10SubmissionCall(t, store, account.ID, "submission-replay", "a-replay-2")
	secondResult := billing.CallRatingResult{CallID: secondCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-replay-2", CustomerValuation: phase10SubmissionValuation(t, secondCall, 5, 10, "tariff-v1", "policy-v1")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: secondCall, Exposure: secondExposure, Result: secondResult}); err != nil {
		t.Fatal(err)
	}

	otherCall, otherExposure := phase10SubmissionCall(t, store, otherAccount.ID, "submission-replay", "a-replay-other-account")
	otherResult := billing.CallRatingResult{CallID: otherCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-replay-other-account", CustomerValuation: phase10SubmissionValuation(t, otherCall, 5, 10, "tariff-v1", "policy-v1")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: otherCall, Exposure: otherExposure, Result: otherResult}); err != nil {
		t.Fatal(err)
	}
	newSubmissionCall, newSubmissionExposure := phase10SubmissionCall(t, store, account.ID, "submission-new", "a-replay-new")
	newSubmissionResult := billing.CallRatingResult{CallID: newSubmissionCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-replay-new", CustomerValuation: phase10SubmissionValuation(t, newSubmissionCall, 5, 10, "tariff-v1", "policy-v1")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: newSubmissionCall, Exposure: newSubmissionExposure, Result: newSubmissionResult}); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 960 {
		t.Fatalf("same-account balance = %d, want 960 (15 + 10 + 15)", got.BalanceNano)
	}
	got, err = store.GetAccount(ctx, otherAccount.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 985 {
		t.Fatalf("other-account balance = %d, want 985", got.BalanceNano)
	}
	var claimKey string
	if err := store.db.NewRaw(`SELECT claim_key FROM billing_submission_fee_claims WHERE store_id = ? AND account_id = ? AND submission_id = ?`, "test", account.ID, "submission-replay").Scan(ctx, &claimKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`UPDATE billing_submission_fee_claims SET amount_nano = amount_nano + 1 WHERE claim_key = ?`, claimKey).Exec(ctx); err == nil {
		t.Fatal("submission fee claim update unexpectedly succeeded")
	}
	if _, err := store.db.NewRaw(`DELETE FROM billing_submission_fee_claims WHERE claim_key = ?`, claimKey).Exec(ctx); err == nil {
		t.Fatal("submission fee claim delete unexpectedly succeeded")
	}
}

func TestSQLiteSubmissionFeeClaimIsNotPersistedWhenSettlementReconciles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteTestStore(t)
	account := billing.Account{ID: "submission-rollback", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	failedCall, failedExposure := phase10SubmissionCallWithMax(t, store, account.ID, "submission-rollback", "a-rollback-failed", 10)
	failedResult := billing.CallRatingResult{CallID: failedCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-rollback-failed", CustomerValuation: phase10SubmissionValuation(t, failedCall, 5, 10, "tariff-v1", "policy-v1")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: failedCall, Exposure: failedExposure, Result: failedResult}); !errors.Is(err, billing.ErrSettlementReconcileRequired) {
		t.Fatalf("failed settlement = %v, want reconciliation", err)
	}
	if _, err := store.db.NewRaw(`UPDATE billing_accounts SET state = 'ready' WHERE account_id = ?`, account.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	validCall, validExposure := phase10SubmissionCallWithMax(t, store, account.ID, "submission-rollback", "a-rollback-valid", 20)
	validResult := billing.CallRatingResult{CallID: validCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-rollback-valid", CustomerValuation: phase10SubmissionValuation(t, validCall, 5, 10, "tariff-v1", "policy-v1")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: validCall, Exposure: validExposure, Result: validResult}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 85 {
		t.Fatalf("balance after retry following rollback = %d, want 85", got.BalanceNano)
	}
	var claims int
	if err := store.db.NewRaw(`SELECT COUNT(*) FROM billing_submission_fee_claims WHERE store_id = ? AND account_id = ? AND submission_id = ?`, "test", account.ID, "submission-rollback").Scan(ctx, &claims); err != nil {
		t.Fatal(err)
	}
	if claims != 1 {
		t.Fatalf("rollback claim count = %d, want one after successful retry", claims)
	}
}

func TestSQLiteSubmissionFeeClaimIsolatedByStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	left, right := newSQLiteTestStore(t), newSQLiteTestStore(t)
	account := billing.Account{ID: "submission-store-isolation", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	for _, store := range []*DurableStore{left, right} {
		if err := store.CreateAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		call, exposure := phase10SubmissionCall(t, store, account.ID, "submission-store", "a-store")
		result := billing.CallRatingResult{CallID: call.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-store-result", CustomerValuation: phase10SubmissionValuation(t, call, 5, 10, "tariff-v1", "policy-v1")}
		if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result}); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetAccount(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.BalanceNano != 85 {
			t.Fatalf("isolated store balance = %d, want 85", got.BalanceNano)
		}
	}
}

func TestSQLiteSubmissionFeeSettlementRejectsFrozenTariffOrPolicyConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteTestStore(t)
	account := billing.Account{ID: "submission-context-conflict", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	firstCall, firstExposure := phase10SubmissionCall(t, store, account.ID, "submission-context", "a-context-1")
	firstResult := billing.CallRatingResult{CallID: firstCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-context-1", CustomerValuation: phase10SubmissionValuation(t, firstCall, 5, 10, "tariff-v1", "policy-v1")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: firstCall, Exposure: firstExposure, Result: firstResult}); err != nil {
		t.Fatal(err)
	}

	secondCall, secondExposure := phase10SubmissionCall(t, store, account.ID, "submission-context", "a-context-2")
	secondResult := billing.CallRatingResult{CallID: secondCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-context-2", CustomerValuation: phase10SubmissionValuation(t, secondCall, 5, 10, "tariff-v2", "policy-v1")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: secondCall, Exposure: secondExposure, Result: secondResult}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("tariff conflict = %v, want ErrOperationConflict", err)
	}
	thirdCall, thirdExposure := phase10SubmissionCall(t, store, account.ID, "submission-context-policy", "a-context-3")
	thirdResult := billing.CallRatingResult{CallID: thirdCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-context-3", CustomerValuation: phase10SubmissionValuation(t, thirdCall, 5, 10, "tariff-v1", "policy-v2")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: thirdCall, Exposure: thirdExposure, Result: thirdResult}); err != nil {
		t.Fatalf("new submission with policy context: %v", err)
	}

	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 970 {
		t.Fatalf("balance after context tests = %d, want 970 (first + distinct submission)", got.BalanceNano)
	}
}

func phase10SubmissionCall(t *testing.T, store *DurableStore, accountID, submissionID, aLegID string) (billing.CallUsageRecord, billing.CallExposure) {
	return phase10SubmissionCallWithMax(t, store, accountID, submissionID, aLegID, 100)
}

func phase10SubmissionCallWithMax(t *testing.T, store *DurableStore, accountID, submissionID, aLegID string, maxNano int64) (billing.CallUsageRecord, billing.CallExposure) {
	t.Helper()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID, ALegID: aLegID,
		SessionID: "session-" + aLegID, SubmissionID: submissionID, StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(), Outcome: billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
	}
	if err := store.AppendCallUsage(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(context.Background(), billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(), Max: billing.Money{Nano: maxNano, Currency: "USD"}, PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	return call, exposure
}

func phase10SubmissionValuation(t *testing.T, call billing.CallUsageRecord, submissionFee, callFee int64, tariffVersion, policyVersion string) economics.Valuation {
	t.Helper()
	ref := metering.ObservationRef{StoreID: "test", ObservationID: "observation-" + call.CallID.String(), Revision: 1, PayloadHash: "payload-" + call.CallID.String()}
	inputHash, err := economics.CanonicalInputSetHash(economics.BasisCustomerPolicy, []metering.ObservationRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	tariffHash := phase10SubmissionHash("tariff", tariffVersion)
	policyHash := phase10SubmissionHash("policy", policyVersion)
	qualifierHash := phase10SubmissionHash("qualifier", tariffVersion+"/"+policyVersion)
	lines := []economics.LineItem{
		phase10SubmissionLine(t, "fixed:submission-fee", "submission-fee", economics.FixedFeeScopeSubmission, submissionFee, tariffVersion),
		phase10SubmissionLine(t, "fixed:call-fee", "call-fee", economics.FixedFeeScopeCall, callFee, tariffVersion),
	}
	total := submissionFee + callFee
	totalDecimal := phase10SubmissionDecimal(t, total)
	return economics.Valuation{
		ID: "valuation-" + call.CallID.String(), Version: economics.ValuationVersionV2, Perspective: metering.PerspectiveCustomer, Basis: economics.BasisCustomerPolicy,
		Subject: metering.SubjectRef{Kind: metering.SubjectBillingCall, StoreID: "test", BillingCallID: call.CallID.String()}, Scope: "call:" + call.CallID.String(),
		InputObservations: []metering.ObservationRef{ref}, InputSetHash: inputHash,
		Rater:                economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rater", Version: tariffVersion}, RaterID: "reference"},
		RaterContent:         &economics.SnapshotContentRef{ContentRef: "rater://" + tariffVersion, ContentHash: tariffHash},
		Tariff:               economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff", Version: tariffVersion}, RaterID: "reference"},
		TariffContent:        &economics.SnapshotContentRef{ContentRef: "tariff://" + tariffVersion, ContentHash: tariffHash},
		Policy:               economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy", Version: policyVersion}, PolicyID: "policy"},
		PolicyContent:        &economics.SnapshotContentRef{ContentRef: "policy://" + policyVersion, ContentHash: policyHash},
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "qualifier://" + tariffVersion + "/" + policyVersion, ContentHash: qualifierHash},
		Lines:                lines, Totals: []economics.CurrencyTotal{{Currency: "USD", Amount: totalDecimal, RoundedAmount: economics.Money{NanoUnits: total, Currency: "USD", Present: true}}},
		Completeness: economics.CompletenessComplete, CreatedAt: time.Unix(101, 0).UTC(),
	}
}

func phase10SubmissionLine(t *testing.T, id, ruleID string, scope economics.FixedFeeScope, nano int64, version string) economics.LineItem {
	t.Helper()
	amount := phase10SubmissionDecimal(t, nano)
	return economics.LineItem{
		ID: id, RuleID: ruleID, ItemID: ruleID, Unit: metering.UnitCount,
		FixedFee: &economics.FixedFeeIdentity{ID: ruleID, Scope: scope, Version: version}, Amount: amount,
		RoundedAmount: &economics.Money{NanoUnits: nano, Currency: "USD", Present: true}, RoundingScope: economics.RoundingScopeLine,
		RoundingPolicy: economics.RoundingHalfAwayFromZero, Status: economics.RatingLineRated, ChargeKind: "commercial_fee",
	}
}

func phase10SubmissionDecimal(t *testing.T, nano int64) *metering.Decimal {
	t.Helper()
	whole := nano / 1_000_000_000
	frac := nano % 1_000_000_000
	value, err := metering.ParseDecimal(fmt.Sprintf("%d.%09d", whole, frac))
	if err != nil {
		t.Fatal(err)
	}
	return &value
}

func phase10SubmissionHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
