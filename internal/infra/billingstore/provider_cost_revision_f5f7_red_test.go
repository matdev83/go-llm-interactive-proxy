package billingstore

// Phase 17.3 F5+F7 RED: immutable provider revision ownership + atomic legacy handoff.
// These tests assert the REQUIRED fixed behavior. On pre-fix code they MUST FAIL,
// demonstrating F5 (completed V1 pin authorizes new money after active) and
// F7 (handoff commits money without completing pin).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func f5f7RevisionInput(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, headKey string, revision uint64, amount int64, payable bool) billing.ProviderCostRevisionInput {
	t.Helper()
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: store.StoreID(), AccountID: accountID,
		ALegID: "a-f5f7", BillingCallID: callID.String(), BLegID: "b-f5f7",
	}
	amountDecimal := metering.DecimalFromNanoUnits(amount)
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	if !payable {
		payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer"}
	}
	evidence := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: subject, Scope: "f5f7-provider-cost", Payer: payer,
		Observations: []metering.Observation{{
			Version: metering.ObservationVersionV2, ID: fmt.Sprintf("f5f7-provider-charge-%d", revision),
			SourceEventKey: fmt.Sprintf("f5f7-provider-charge-%d", revision), Revision: revision,
			StreamID: "f5f7-provider-stream", Sequence: revision, Origin: metering.OriginProvider,
			Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
			Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
			Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
			Correlation: metering.CorrelationV2{
				StoreID: subject.StoreID, ALegID: subject.ALegID,
				BillingCallID: subject.BillingCallID, BLegID: subject.BLegID,
			},
			Semantics: metering.SemanticsCumulative, ObservedAt: time.Unix(100, 0).UTC(),
			ReceivedAt: time.Unix(100, 0).UTC(), MappingRef: "f5f7.provider.cost",
			Charges: []metering.ReportedCharge{{
				ChargeItemID: "provider-charge", Kind: metering.ChargeKindAggregate,
				Amount: &amountDecimal, Currency: "USD", Payer: payer,
			}},
		}},
		Rater: economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "f5f7-rater", Version: "v1"}, RaterID: "reference"},
	}
	cost := billing.OperatorCOGSResult{
		KnownSubtotalByCurrency: map[string]billing.Money{"USD": {Nano: amount, Currency: "USD"}},
		KnownSubtotal:           billing.Money{Nano: amount, Currency: "USD"},
		Completeness:            billing.CostCompletenessKnown,
		Payable:                 payable,
		IncludedLegKeys:         []string{"b-f5f7"},
	}
	if !payable {
		cost.IncludedLegKeys = nil
	}
	return billing.ProviderCostRevisionInput{
		AccountID: accountID, CallID: callID, Subject: subject, HeadKey: headKey,
		EvidenceRevision: revision, InputSetHash: fmt.Sprintf("%064x", revision),
		ValuationID: fmt.Sprintf("f5f7-provider-valuation-%d", revision), Cost: cost,
		Authoritative: payable, Evidence: evidence,
	}
}

func f5f7NewStore(t *testing.T, storeID string) *DurableStore {
	t.Helper()
	base := newSQLiteTestStore(t)
	if base.StoreID() == storeID {
		return base
	}
	s, err := NewDurableStore(context.Background(), base.DB(), Config{StoreID: storeID})
	if err != nil {
		t.Fatalf("NewDurableStore %q: %v", storeID, err)
	}
	return s
}

func f5f7Activate(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f5f7-shadow",
	})
	if err != nil {
		t.Fatal(err)
	}
	dr, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: sh.Version, ExpectedEpoch: sh.Epoch,
		NextState: billing.AccountingCutoverV1Draining, TransitionID: "f5f7-drain",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = dr
	cur, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: cur.Version, ExpectedEpoch: cur.Epoch,
		NextState: billing.AccountingCutoverV2Active, TransitionID: "f5f7-active",
	}); err != nil {
		t.Fatal(err)
	}
}

func f5f7ProviderJournals(t *testing.T, store *DurableStore, accountID string) int {
	t.Helper()
	txs, err := store.JournalTransactions(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tx := range txs {
		if tx.OperationKind == "provider_call_cogs" {
			n++
		}
	}
	return n
}

func f5f7IsFenceErr(err error) bool {
	return errors.Is(err, billing.ErrPostingOwnershipFence) ||
		errors.Is(err, billing.ErrPostingOwnershipConflict) ||
		errors.Is(err, billing.ErrAccountingCutoverFence) ||
		errors.Is(err, billing.ErrCutoverV1Fenced) ||
		errors.Is(err, billing.ErrCutoverV2NotAuthorized) ||
		errors.Is(err, ErrOperationConflict) ||
		errors.Is(err, billing.ErrProviderCostRevisionConflict) ||
		errors.Is(err, billing.ErrProviderCostRevisionFence)
}

// F5: completed V1 base then v2_active higher V1 with changed amount must fence.
func TestF5F7Red_CompletedV1BaseThenActiveHigherV1Fenced(t *testing.T) {
	t.Parallel()
	store := f5f7NewStore(t, "f5f7-red-f5")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-f5f7-red-f5", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	base := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-red-head", 1, 20, true)
	if _, err := store.ApplyProviderCostRevision(ctx, base); err != nil {
		t.Fatalf("seed V1 base: %v", err)
	}
	f5f7Activate(t, store)
	higher := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-red-head", 2, 99, true)
	if _, err := store.ApplyProviderCostRevision(ctx, higher); !f5f7IsFenceErr(err) {
		t.Fatalf("F5 RED: active higher V1 posted (journals=%d) err=%v, want fence", f5f7ProviderJournals(t, store, acct.ID), err)
	}
	if n := f5f7ProviderJournals(t, store, acct.ID); n != 1 {
		t.Fatalf("F5 RED: journals=%d want 1 (no new money)", n)
	}
}

// F5 changed-evidence: same lineage higher revision different hash with V1 owner fenced after active.
func TestF5F7Red_ChangedEvidenceFencedAfterActive(t *testing.T) {
	t.Parallel()
	store := f5f7NewStore(t, "f5f7-red-ev")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-f5f7-red-ev", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	base := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-ev-head", 1, 20, true)
	if _, err := store.ApplyProviderCostRevision(ctx, base); err != nil {
		t.Fatalf("seed: %v", err)
	}
	f5f7Activate(t, store)
	changed := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-ev-head", 2, 35, true)
	changed.InputSetHash = fmt.Sprintf("%064x", 9999)
	changed.ValuationID = "f5f7-provider-valuation-changed"
	if _, err := store.ApplyProviderCostRevision(ctx, changed); !f5f7IsFenceErr(err) {
		t.Fatalf("F5 RED changed-evidence: active V1 posted err=%v, want fence", err)
	}
}

// F7: legacy-to-revision handoff must complete revision pin atomically in same tx.
func TestF5F7Red_HandoffLeavesPinCompleted(t *testing.T) {
	t.Parallel()
	store := f5f7NewStore(t, "f5f7-red-handoff")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-f5f7-red-handoff", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := testIndependentCallUsageFor(callID, []string{"b-f5f7"})
	call.AccountID = acct.ID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-f5f7")
	leg.ALegID = "a-f5f7"
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	legacyResult := billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 30, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: acct.ID, CallID: callID, Leg: leg, Result: legacyResult}); err != nil {
		t.Fatalf("legacy base: %v", err)
	}
	rev := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-handoff-head", 2, 45, true)
	rev.Subject.BLegID = "b-f5f7"
	rev.Subject.ALegID = "a-f5f7"
	res, err := store.ApplyProviderCostRevision(ctx, rev)
	if err != nil {
		t.Fatalf("handoff revision: %v", err)
	}
	if !res.Applied {
		t.Fatalf("handoff must apply, got %+v", res)
	}
	// Revision-specific pin must exist and be completed in same tx.
	srcKey, err := billing.ProviderCostRevisionSourceKey(rev)
	if err != nil {
		t.Fatal(err)
	}
	revOpKey := billing.ScopedOperationKey("provider_call_cogs", acct.ID, srcKey)
	// Try lineage key too for diagnostics; require at least revision pin completed.
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, revOpKey)
	if err != nil {
		t.Fatalf("F7 RED: handoff revision pin missing for %q: %v (commit left pin pinned/missing)", revOpKey, err)
	}
	if !pin.IsCompleted() {
		t.Fatalf("F7 RED: handoff pin must be completed atomically, got %#v", pin)
	}
	if n := f5f7ProviderJournals(t, store, acct.ID); n != 2 {
		t.Fatalf("F7 RED: journals=%d want 2 (legacy+delta)", n)
	}
}

// F7 crash/reopen immediately after handoff on same file.
func TestF5F7Red_HandoffCrashReopenPreservesPin(t *testing.T) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "f5f7-red-reopen.db")))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: "f5f7-red-reopen"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	ctx := context.Background()
	acct := billing.Account{ID: "acct-f5f7-red-reopen", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := testIndependentCallUsageFor(callID, []string{"b-f5f7"})
	call.AccountID = acct.ID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-f5f7")
	leg.ALegID = "a-f5f7"
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	legacyResult := billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 30, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: acct.ID, CallID: callID, Leg: leg, Result: legacyResult}); err != nil {
		t.Fatalf("legacy: %v", err)
	}
	rev := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-reopen-head", 2, 45, true)
	rev.Subject.BLegID = "b-f5f7"
	rev.Subject.ALegID = "a-f5f7"
	if _, err := store.ApplyProviderCostRevision(ctx, rev); err != nil {
		t.Fatalf("handoff: %v", err)
	}
	// Crash: close and reopen same file immediately.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	sqlDB2, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB2.SetMaxOpenConns(4)
	bunDB2, err := db.NewBunDB(sqlDB2, db.DialectSQLite)
	if err != nil {
		_ = sqlDB2.Close()
		t.Fatal(err)
	}
	reopened, err := NewDurableStore(ctx, bunDB2, Config{StoreID: "f5f7-red-reopen"})
	if err != nil {
		_ = bunDB2.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	srcKey, err := billing.ProviderCostRevisionSourceKey(rev)
	if err != nil {
		t.Fatal(err)
	}
	revOpKey := billing.ScopedOperationKey("provider_call_cogs", acct.ID, srcKey)
	pin, err := reopened.GetPostingPin(ctx, billing.PostingOperationProviderCharge, revOpKey)
	if err != nil {
		t.Fatalf("F7 RED reopen: revision pin missing after crash/reopen: %v", err)
	}
	if !pin.IsCompleted() {
		t.Fatalf("F7 RED reopen: pin must stay completed, got %#v", pin)
	}
	head, err := reopened.GetProviderCostHead(ctx, acct.ID, callID, rev.HeadKey)
	if err != nil {
		t.Fatalf("reopen head: %v", err)
	}
	if head.CurrentAmount.Nano != 45 {
		t.Fatalf("reopen head amount=%d want 45", head.CurrentAmount.Nano)
	}
	// Exact replay after reopen must be idempotent without new money.
	replay, err := reopened.ApplyProviderCostRevision(ctx, rev)
	if err != nil {
		t.Fatalf("reopen replay: %v", err)
	}
	if !replay.Replayed {
		t.Fatalf("reopen replay must be replayed, got %+v", replay)
	}
}
