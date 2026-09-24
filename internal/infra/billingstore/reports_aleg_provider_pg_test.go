//go:build integration

package billingstore

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// TestPostgresProviderLoaderLegBoundFifty mirrors the SQLite
// per-leg SQL bound proof on direct PostgreSQL: 50 distinct valid
// children on one leg materialize at most 9 head and fence rows for
// that leg through the PostgreSQL JSON/lineage window expressions,
// while a neighbor call reusing charge IDs across legs and mixing
// subject kinds loads completely and resolves known.
func TestPostgresProviderLoaderLegBoundFifty(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 8)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const accountID, aLegID = "aleg-pg-legbound", "a-leg-pg"
	require.NoError(t, store.CreateAccount(ctx, billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 10_000_000, State: billing.AccountReady, Version: 1,
	}))
	newPGCall := func() billing.BillingCallID {
		t.Helper()
		callID, err := billing.NewBillingCallID()
		require.NoError(t, err)
		require.NoError(t, store.AppendCallUsage(ctx, billing.CallUsageRecord{
			SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
			AccountID: accountID, ALegID: aLegID, SessionID: "sess-" + aLegID,
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
			Outcome:            billing.TurnOutcomeCompleted,
			CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"},
			ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v1"},
		}))
		return callID
	}
	seedPGLeg := func(callID billing.BillingCallID, bLegID string, seq int, outcome billing.LegOutcome) billing.CallLegUsageRecord {
		t.Helper()
		leg := billing.CallLegUsageRecord{
			CallID: callID, ALegID: aLegID, BLegID: bLegID, AttemptSeq: seq,
			BackendID: "ok", ProviderID: "provider-a", ModelID: "model-a",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
			Outcome: outcome, Surfaced: billing.SurfacedYes,
		}
		require.NoError(t, store.AppendCallLegUsage(ctx, leg))
		sealed, err := leg.Seal()
		require.NoError(t, err)
		return sealed
	}
	applyPG := func(input billing.ProviderCostRevisionInput) billing.ProviderCostRevisionResult {
		t.Helper()
		result, err := store.ApplyProviderCostRevision(ctx, input)
		require.NoError(t, err)
		require.True(t, result.Applied)
		return result
	}
	claimPG := func(callID billing.BillingCallID, leg billing.CallLegUsageRecord) {
		t.Helper()
		claimed, err := store.ClaimProviderCostWorkForRevision(ctx, billing.ProviderCostWork{AccountID: accountID, CallID: callID, Leg: leg})
		require.NoError(t, err)
		require.True(t, claimed)
	}

	floodCall := newPGCall()
	floodLeg := seedPGLeg(floodCall, "b-flood", 1, billing.LegOutcomeWinner)
	for i := 0; i < 50; i++ {
		chargeID := fmt.Sprintf("charge-%02d", i)
		applyPG(c2r2ChildRevisionInput(t, accountID, floodCall, aLegID, "b-flood", chargeID, "head-"+chargeID, 1, 1, true))
	}
	claimPG(floodCall, floodLeg)

	multiCall := newPGCall()
	legOne := seedPGLeg(multiCall, "b-1", 1, billing.LegOutcomeWinner)
	legTwo := seedPGLeg(multiCall, "b-2", 2, billing.LegOutcomeFailed)
	legAgg := seedPGLeg(multiCall, "b-3", 3, billing.LegOutcomeWinner)
	applyPG(c2r2ChildRevisionInput(t, accountID, multiCall, aLegID, "b-1", "charge-00", "head-m1-a", 1, 30, true))
	applyPG(c2r2ChildRevisionInput(t, accountID, multiCall, aLegID, "b-1", "charge-01", "head-m1-b", 1, 20, true))
	applyPG(c2r2ChildRevisionInput(t, accountID, multiCall, aLegID, "b-2", "charge-00", "head-m2-a", 1, 7, true))
	applyPG(c2bProviderRevisionInput(accountID, multiCall, aLegID, "b-3", "head-agg", 1, 5, true))
	claimPG(multiCall, legOne)
	claimPG(multiCall, legTwo)
	claimPG(multiCall, legAgg)

	callIDs := []string{floodCall.String(), multiCall.String()}
	heads, _, err := loadALegProviderHeadsTx(ctx, store.db, "test", accountID, callIDs)
	require.NoError(t, err)
	require.Len(t, heads[floodCall.String()], 9,
		"PostgreSQL must return exactly cap+1 head rows for the flood leg")
	for _, callID := range callIDs {
		byLeg := make(map[string]int)
		for _, row := range heads[callID] {
			var subject struct {
				BLegID string `json:"b_leg_id"`
			}
			require.NoError(t, json.Unmarshal([]byte(row.SubjectJSON), &subject))
			byLeg[subject.BLegID]++
		}
		for bleg, count := range byLeg {
			require.LessOrEqual(t, count, 9, "leg %s of call %s exceeds the SQL bound", bleg, callID)
		}
	}
	posting, _, err := loadALegProviderPostingFencesTx(ctx, store.db, "test", accountID, callIDs)
	require.NoError(t, err)
	require.Len(t, posting[floodCall.String()], 9,
		"PostgreSQL must return exactly cap+1 posting fences for the flood leg")
	require.Len(t, posting[multiCall.String()], 4)
	execution, _, err := loadALegProviderExecutionFencesTx(ctx, store.db, "test", accountID, callIDs)
	require.NoError(t, err)
	require.Len(t, execution[floodCall.String()], 1)
	require.Len(t, execution[multiCall.String()], 3)

	report, err := store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.NoError(t, err)
	byLeg := make(map[string]billing.ALegReportLeg)
	for i := range report.Calls {
		for j := range report.Calls[i].BLegs {
			leg := report.Calls[i].BLegs[j]
			byLeg[report.Calls[i].CallID.String()+"/"+leg.BLegID] = leg
		}
	}
	flood := byLeg[floodCall.String()+"/b-flood"]
	require.Equal(t, billing.ALegProviderUnknown, flood.ProviderStatus)
	require.Empty(t, flood.ProviderChildren)
	require.Equal(t, billing.ALegProviderKnown, byLeg[multiCall.String()+"/b-1"].ProviderStatus)
	require.Equal(t, int64(50), byLeg[multiCall.String()+"/b-1"].ProviderCost.Nano)
	require.Equal(t, billing.ALegProviderKnown, byLeg[multiCall.String()+"/b-2"].ProviderStatus)
	require.Equal(t, int64(7), byLeg[multiCall.String()+"/b-2"].ProviderCost.Nano)
	require.Equal(t, billing.ALegProviderKnown, byLeg[multiCall.String()+"/b-3"].ProviderStatus)
	require.Equal(t, int64(5), byLeg[multiCall.String()+"/b-3"].ProviderCost.Nano)
	require.Equal(t, int64(62), report.Provider.KnownSubtotal.Nano)
	require.Equal(t, 3, report.Provider.KnownLegs)
	require.Equal(t, 1, report.Provider.UnknownLegs)
}

// newPGPoisonStore opens an isolated direct-PostgreSQL store with
// one prepaid account for adversarial tests.
func newPGPoisonStore(t *testing.T, ctx context.Context, accountID string) *DurableStore {
	t.Helper()
	dsn := testkit.SkipUnlessPostgres(t)
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 8)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.CreateAccount(ctx, billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 10_000_000, State: billing.AccountReady, Version: 1,
	}))
	return store
}

// seedPGPoisonCall builds a call with two known legs (b-1 at 30,
// b-2 at 7) plus a neighbor call with one known leg (b-1 at 5) on
// direct PostgreSQL for adversarial tests.
func seedPGPoisonCall(t *testing.T, ctx context.Context, store *DurableStore, accountID, aLegID string) (poisonCall, neighborCall billing.BillingCallID) {
	t.Helper()
	newPGCall := func() billing.BillingCallID {
		t.Helper()
		callID, err := billing.NewBillingCallID()
		require.NoError(t, err)
		require.NoError(t, store.AppendCallUsage(ctx, billing.CallUsageRecord{
			SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
			AccountID: accountID, ALegID: aLegID, SessionID: "sess-" + aLegID,
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
			Outcome:            billing.TurnOutcomeCompleted,
			CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"},
			ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v1"},
		}))
		return callID
	}
	seedPGLeg := func(callID billing.BillingCallID, bLegID string, seq int, outcome billing.LegOutcome) billing.CallLegUsageRecord {
		t.Helper()
		leg := billing.CallLegUsageRecord{
			CallID: callID, ALegID: aLegID, BLegID: bLegID, AttemptSeq: seq,
			BackendID: "ok", ProviderID: "provider-a", ModelID: "model-a",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
			Outcome: outcome, Surfaced: billing.SurfacedYes,
		}
		require.NoError(t, store.AppendCallLegUsage(ctx, leg))
		sealed, err := leg.Seal()
		require.NoError(t, err)
		return sealed
	}
	applyPG := func(input billing.ProviderCostRevisionInput) {
		t.Helper()
		result, err := store.ApplyProviderCostRevision(ctx, input)
		require.NoError(t, err)
		require.True(t, result.Applied)
	}
	claimPG := func(callID billing.BillingCallID, leg billing.CallLegUsageRecord) {
		t.Helper()
		claimed, err := store.ClaimProviderCostWorkForRevision(ctx, billing.ProviderCostWork{AccountID: accountID, CallID: callID, Leg: leg})
		require.NoError(t, err)
		require.True(t, claimed)
	}
	poisonCall = newPGCall()
	legOne := seedPGLeg(poisonCall, "b-1", 1, billing.LegOutcomeWinner)
	legTwo := seedPGLeg(poisonCall, "b-2", 2, billing.LegOutcomeFailed)
	applyPG(c2r2ChildRevisionInput(t, accountID, poisonCall, aLegID, "b-1", "charge-a", "head-poison-a", 1, 30, true))
	applyPG(c2r2ChildRevisionInput(t, accountID, poisonCall, aLegID, "b-2", "charge-b", "head-poison-b", 1, 7, true))
	claimPG(poisonCall, legOne)
	claimPG(poisonCall, legTwo)

	neighborCall = newPGCall()
	legN := seedPGLeg(neighborCall, "b-1", 1, billing.LegOutcomeWinner)
	applyPG(c2r2ChildRevisionInput(t, accountID, neighborCall, aLegID, "b-1", "charge-n", "head-neighbor", 1, 5, true))
	claimPG(neighborCall, legN)
	return poisonCall, neighborCall
}

// findPGProviderLeg locates one leg verdict on the report page.
func findPGProviderLeg(t *testing.T, report billing.ALegReport, callID billing.BillingCallID, bLegID string) billing.ALegReportLeg {
	t.Helper()
	for _, row := range report.Calls {
		if row.CallID != callID {
			continue
		}
		for _, leg := range row.BLegs {
			if leg.BLegID == bLegID {
				return leg
			}
		}
	}
	t.Fatalf("leg %q of call %q not on page", bLegID, callID.String())
	return billing.ALegReportLeg{}
}

// requirePGCallPoisoned asserts the poisoned call contributes no
// provider evidence while the neighbor call stays exactly known.
func requirePGCallPoisoned(t *testing.T, report billing.ALegReport, poisonCall, neighborCall billing.BillingCallID) {
	t.Helper()
	for _, bleg := range []string{"b-1", "b-2"} {
		resolved := findPGProviderLeg(t, report, poisonCall, bleg)
		require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus, "poisoned leg %s must stay unknown", bleg)
		require.Empty(t, resolved.ProviderChildren, "poisoned leg %s must expose no partial lineage", bleg)
	}
	requireALegIssue(t, report, "provider_cost_fanout")
	neighbor := findPGProviderLeg(t, report, neighborCall, "b-1")
	require.Equal(t, billing.ALegProviderKnown, neighbor.ProviderStatus)
	require.Equal(t, int64(5), neighbor.ProviderCost.Nano)
	requireProviderLegTotals(t, report, 5, 1, 0, 0, 2)
}

// TestPostgresProviderEmptySubjectPoisonsCall mirrors the SQLite
// empty-subject poison proof on direct PostgreSQL.
func TestPostgresProviderEmptySubjectPoisonsCall(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	const accountID, aLegID = "aleg-pg-poisonempty", "a-leg-pg"
	store := newPGPoisonStore(t, ctx, accountID)
	poisonCall, neighborCall := seedPGPoisonCall(t, ctx, store, accountID, aLegID)
	plantProviderHeadRow(t, store, accountID, poisonCall, "head-empty", "provider_charge", "charge-ghost", "{}")

	report, err := store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.NoError(t, err)
	requirePGCallPoisoned(t, report, poisonCall, neighborCall)
}

// TestPostgresProviderForeignBLegHeadPoisonsCall mirrors the SQLite
// foreign-B-leg head poison proof on direct PostgreSQL.
func TestPostgresProviderForeignBLegHeadPoisonsCall(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	const accountID, aLegID = "aleg-pg-poisonhead", "a-leg-pg"
	store := newPGPoisonStore(t, ctx, accountID)
	poisonCall, neighborCall := seedPGPoisonCall(t, ctx, store, accountID, aLegID)
	subject := metering.SubjectRef{
		Kind: metering.SubjectProviderCharge, StoreID: "test", AccountID: accountID,
		ALegID: aLegID, BillingCallID: poisonCall.String(), BLegID: "b-ghost",
		ProviderAccountKey: "provider-acct", ProviderChargeID: "charge-ghost",
	}
	payload, err := json.Marshal(subject)
	require.NoError(t, err)
	plantProviderHeadRow(t, store, accountID, poisonCall, "head-ghost", "provider_charge", "charge-ghost", string(payload))

	report, err := store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.NoError(t, err)
	requirePGCallPoisoned(t, report, poisonCall, neighborCall)
}

// TestPostgresProviderMalformedFenceLineagePoisonsLeg mirrors the
// SQLite exact-leg fence poison proof on direct PostgreSQL.
func TestPostgresProviderMalformedFenceLineagePoisonsLeg(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	const accountID, aLegID = "aleg-pg-poisonfence", "a-leg-pg"
	store := newPGPoisonStore(t, ctx, accountID)
	poisonCall, _ := seedPGPoisonCall(t, ctx, store, accountID, aLegID)
	plantPostingFenceRow(t, store, accountID, poisonCall, poisonCall.String()+":b-1:GARBAGE", "head-ghost")

	report, err := store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.NoError(t, err)
	resolved := findPGProviderLeg(t, report, poisonCall, "b-1")
	require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus)
	require.Empty(t, resolved.ProviderChildren)
	requireALegIssue(t, report, "provider_cost_fanout")
	sibling := findPGProviderLeg(t, report, poisonCall, "b-2")
	require.Equal(t, billing.ALegProviderKnown, sibling.ProviderStatus)
	require.Equal(t, int64(7), sibling.ProviderCost.Nano)
}

// TestPostgresProviderForeignFenceLineagePoisonsCall mirrors the
// SQLite foreign-lineage call poison proof on direct PostgreSQL.
func TestPostgresProviderForeignFenceLineagePoisonsCall(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	const accountID, aLegID = "aleg-pg-poisonforeign", "a-leg-pg"
	store := newPGPoisonStore(t, ctx, accountID)
	poisonCall, neighborCall := seedPGPoisonCall(t, ctx, store, accountID, aLegID)
	plantPostingFenceRow(t, store, accountID, poisonCall, poisonCall.String()+":b-ghost", "head-ghost")

	report, err := store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.NoError(t, err)
	requirePGCallPoisoned(t, report, poisonCall, neighborCall)
}

// TestPostgresProviderMalformedExecLineagePoisonsLeg mirrors the
// SQLite exact-leg execution-fence poison proof on direct
// PostgreSQL.
func TestPostgresProviderMalformedExecLineagePoisonsLeg(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	const accountID, aLegID = "aleg-pg-poisonexec", "a-leg-pg"
	store := newPGPoisonStore(t, ctx, accountID)
	poisonCall, _ := seedPGPoisonCall(t, ctx, store, accountID, aLegID)
	plantExecutionFenceRow(t, store, accountID, poisonCall,
		poisonCall.String()+":b-1:provider-charge:charge-a", "provider_charge", "head-ghost")

	report, err := store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.NoError(t, err)
	resolved := findPGProviderLeg(t, report, poisonCall, "b-1")
	require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus)
	require.Empty(t, resolved.ProviderChildren)
	requireALegIssue(t, report, "provider_cost_fanout")
	sibling := findPGProviderLeg(t, report, poisonCall, "b-2")
	require.Equal(t, billing.ALegProviderKnown, sibling.ProviderStatus)
	require.Equal(t, int64(7), sibling.ProviderCost.Nano)
}

// TestPostgresProviderForeignExecLineagePoisonsCall mirrors the
// SQLite foreign execution-lineage call poison proof on direct
// PostgreSQL.
func TestPostgresProviderForeignExecLineagePoisonsCall(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	const accountID, aLegID = "aleg-pg-poisonexecforeign", "a-leg-pg"
	store := newPGPoisonStore(t, ctx, accountID)
	poisonCall, neighborCall := seedPGPoisonCall(t, ctx, store, accountID, aLegID)
	plantExecutionFenceRow(t, store, accountID, poisonCall,
		poisonCall.String()+":b-ghost", "provider_charge", "head-ghost")

	report, err := store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.NoError(t, err)
	requirePGCallPoisoned(t, report, poisonCall, neighborCall)
}

// TestPostgresProviderForeignWorkPoisonsCall mirrors the SQLite
// foreign work-key call poison proof on direct PostgreSQL.
func TestPostgresProviderForeignWorkPoisonsCall(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	const accountID, aLegID = "aleg-pg-poisonwork", "a-leg-pg"
	store := newPGPoisonStore(t, ctx, accountID)
	poisonCall, neighborCall := seedPGPoisonCall(t, ctx, store, accountID, aLegID)
	plantWorkRow(t, store, poisonCall, poisonCall.String()+":b-ghost")

	report, err := store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.NoError(t, err)
	requirePGCallPoisoned(t, report, poisonCall, neighborCall)
}

// TestPostgresProviderCrossCallLineagePoisonsLeg mirrors the SQLite
// transplanted-lineage proof on direct PostgreSQL.
func TestPostgresProviderCrossCallLineagePoisonsLeg(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	const accountID, aLegID = "aleg-pg-poisonxcall", "a-leg-pg"
	store := newPGPoisonStore(t, ctx, accountID)
	poisonCall, _ := seedPGPoisonCall(t, ctx, store, accountID, aLegID)
	otherCall, err := billing.NewBillingCallID()
	require.NoError(t, err)
	plantPostingFenceRow(t, store, accountID, poisonCall, otherCall.String()+":b-1", "head-ghost")

	report, err := store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.NoError(t, err)
	resolved := findPGProviderLeg(t, report, poisonCall, "b-1")
	require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus)
	require.Empty(t, resolved.ProviderChildren)
	requireALegIssue(t, report, "provider_cost_fanout")
	sibling := findPGProviderLeg(t, report, poisonCall, "b-2")
	require.Equal(t, billing.ALegProviderKnown, sibling.ProviderStatus)
	require.Equal(t, int64(7), sibling.ProviderCost.Nano)
}

// dupPGBLegSubjectJSON builds a well-formed provider-charge subject
// payload with two top-level b_leg_id keys in the given order,
// mirroring the SQLite duplicate-key fixture.
func dupPGBLegSubjectJSON(accountID, aLegID, callID, first, second string) string {
	return `{"kind":"provider_charge","store_id":"test","account_id":"` + accountID +
		`","a_leg_id":"` + aLegID + `","billing_call_id":"` + callID +
		`","b_leg_id":"` + first + `","provider_account_key":"provider-acct",` +
		`"provider_charge_id":"charge-dup","b_leg_id":"` + second + `"}`
}

// TestPostgresProviderDuplicateSubjectKeysPoisonsCall mirrors the
// SQLite duplicate-key poison proof on direct PostgreSQL: it also
// proves the json_each duplicate detection agrees with Go decoding
// in both key orders, for two enumerated legs, and for same-value
// duplicates.
func TestPostgresProviderDuplicateSubjectKeysPoisonsCall(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	const accountID, aLegID = "aleg-pg-dupkeys", "a-leg-pg"
	store := newPGPoisonStore(t, ctx, accountID)
	poisonCalls := make([]billing.BillingCallID, 0, 4)
	var neighborCall billing.BillingCallID
	duplicates := [][2]string{{"b-1", "b-ghost"}, {"b-ghost", "b-1"}, {"b-1", "b-2"}, {"b-1", "b-1"}}
	for i, dup := range duplicates {
		poisonCall, neighbor := seedPGPoisonCall(t, ctx, store, accountID, aLegID)
		poisonCalls = append(poisonCalls, poisonCall)
		if i == 0 {
			neighborCall = neighbor
		}
		plantProviderHeadRow(t, store, accountID, poisonCall, "head-dup",
			"provider_charge", "charge-dup",
			dupPGBLegSubjectJSON(accountID, aLegID, poisonCall.String(), dup[0], dup[1]))
	}

	report, err := store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.NoError(t, err)
	for _, poisonCall := range poisonCalls {
		for _, bleg := range []string{"b-1", "b-2"} {
			resolved := findPGProviderLeg(t, report, poisonCall, bleg)
			require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus,
				"duplicate-key call %s leg %s must stay unknown", poisonCall.String(), bleg)
			require.Empty(t, resolved.ProviderChildren)
		}
	}
	requireALegIssue(t, report, "provider_cost_fanout")
	neighbor := findPGProviderLeg(t, report, neighborCall, "b-1")
	require.Equal(t, billing.ALegProviderKnown, neighbor.ProviderStatus)
	require.Equal(t, int64(5), neighbor.ProviderCost.Nano)
	requireProviderLegTotals(t, report, 20, 4, 0, 0, 8)
}

// TestPostgresProviderTwoChildrenKnown mirrors the SQLite two-child
// authority proof on direct PostgreSQL: two additive provider-charge
// children submitted through the real revision writer resolve known
// with the checked sum and exact per-child lineage.
func TestPostgresProviderTwoChildrenKnown(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 8)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const accountID, aLegID = "aleg-pg-2child", "a-leg-pg"
	require.NoError(t, store.CreateAccount(ctx, billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 10_000_000, State: billing.AccountReady, Version: 1,
	}))
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	require.NoError(t, store.AppendCallUsage(ctx, billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: accountID, ALegID: aLegID, SessionID: "sess-" + aLegID,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v1"},
	}))
	leg := billing.CallLegUsageRecord{
		CallID: callID, ALegID: aLegID, BLegID: "b-1", AttemptSeq: 1,
		BackendID: "ok", ProviderID: "provider-a", ModelID: "model-a",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
	}
	require.NoError(t, store.AppendCallLegUsage(ctx, leg))
	sealed, err := leg.Seal()
	require.NoError(t, err)

	applyPGChild := func(chargeID, headKey string, amount int64) billing.ProviderCostRevisionResult {
		t.Helper()
		result, err := store.ApplyProviderCostRevision(ctx, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", chargeID, headKey, 1, amount, true))
		require.NoError(t, err)
		require.True(t, result.Applied)
		return result
	}
	applyPGChild("charge-a", "head-charge-a", 30)
	applyPGChild("charge-b", "head-charge-b", 20)
	claimed, err := store.ClaimProviderCostWorkForRevision(ctx, billing.ProviderCostWork{AccountID: accountID, CallID: callID, Leg: sealed})
	require.NoError(t, err)
	require.True(t, claimed)

	report, err := store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.NoError(t, err)
	var resolved *billing.ALegReportLeg
	for i := range report.Calls {
		for j := range report.Calls[i].BLegs {
			if report.Calls[i].BLegs[j].BLegID == "b-1" {
				resolved = &report.Calls[i].BLegs[j]
			}
		}
	}
	require.NotNil(t, resolved)
	require.Equal(t, billing.ALegProviderKnown, resolved.ProviderStatus)
	require.Equal(t, int64(50), resolved.ProviderCost.Nano)
	require.Len(t, resolved.ProviderChildren, 2)
	require.Equal(t, "charge-a", resolved.ProviderChildren[0].ChargeID)
	require.Equal(t, "charge-b", resolved.ProviderChildren[1].ChargeID)
	require.Equal(t, int64(30), resolved.ProviderChildren[0].Amount.Nano)
	require.Equal(t, int64(20), resolved.ProviderChildren[1].Amount.Nano)
	require.Equal(t, int64(50), report.Provider.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Provider.KnownLegs)
}

// TestPostgresProviderOverCapRotationUnknown proves per-subject
// loader overflow stays fail-closed on direct PostgreSQL: two heads
// rotating one charge identity resolve unknown with a fanout issue
// and no subtotal.
func TestPostgresProviderOverCapRotationUnknown(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 8)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const accountID, aLegID = "aleg-pg-overcap", "a-leg-pg"
	require.NoError(t, store.CreateAccount(ctx, billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 10_000_000, State: billing.AccountReady, Version: 1,
	}))
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	require.NoError(t, store.AppendCallUsage(ctx, billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: accountID, ALegID: aLegID, SessionID: "sess-" + aLegID,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v1"},
	}))
	leg := billing.CallLegUsageRecord{
		CallID: callID, ALegID: aLegID, BLegID: "b-1", AttemptSeq: 1,
		BackendID: "ok", ProviderID: "provider-a", ModelID: "model-a",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
	}
	require.NoError(t, store.AppendCallLegUsage(ctx, leg))
	sealed, err := leg.Seal()
	require.NoError(t, err)
	applyPG := func(input billing.ProviderCostRevisionInput) billing.ProviderCostRevisionResult {
		t.Helper()
		result, err := store.ApplyProviderCostRevision(ctx, input)
		require.NoError(t, err)
		require.True(t, result.Applied)
		return result
	}
	applyPG(c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-a", "head-one", 1, 30, true))
	applyPG(c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-a", "head-two", 2, 30, true))
	claimed, err := store.ClaimProviderCostWorkForRevision(ctx, billing.ProviderCostWork{AccountID: accountID, CallID: callID, Leg: sealed})
	require.NoError(t, err)
	require.True(t, claimed)

	report, err := store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.NoError(t, err)
	var resolved *billing.ALegReportLeg
	for i := range report.Calls {
		for j := range report.Calls[i].BLegs {
			if report.Calls[i].BLegs[j].BLegID == "b-1" {
				resolved = &report.Calls[i].BLegs[j]
			}
		}
	}
	require.NotNil(t, resolved)
	require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus)
	require.Empty(t, resolved.ProviderChildren)
	foundFanout := false
	for _, issue := range report.Issues {
		if issue.Code == "provider_cost_fanout" {
			foundFanout = true
		}
	}
	require.True(t, foundFanout, "overflow must carry explicit fanout info, issues = %+v", report.Issues)
	require.Equal(t, int64(0), report.Provider.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Provider.UnknownLegs)
}

// TestPostgresProviderSameChargeTwoLegsKnown mirrors the SQLite
// shared-charge proof on direct PostgreSQL: the same
// provider-charge ID on two B-legs resolves per leg with no false
// overflow from the loader identity windows.
func TestPostgresProviderSameChargeTwoLegsKnown(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 8)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const accountID, aLegID = "aleg-pg-sharedcharge", "a-leg-pg"
	require.NoError(t, store.CreateAccount(ctx, billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 10_000_000, State: billing.AccountReady, Version: 1,
	}))
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	require.NoError(t, store.AppendCallUsage(ctx, billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: accountID, ALegID: aLegID, SessionID: "sess-" + aLegID,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v1"},
	}))
	seedLeg := func(bLegID string, seq int, outcome billing.LegOutcome) billing.CallLegUsageRecord {
		t.Helper()
		leg := billing.CallLegUsageRecord{
			CallID: callID, ALegID: aLegID, BLegID: bLegID, AttemptSeq: seq,
			BackendID: "ok", ProviderID: "provider-a", ModelID: "model-a",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
			Outcome: outcome, Surfaced: billing.SurfacedYes,
		}
		require.NoError(t, store.AppendCallLegUsage(ctx, leg))
		sealed, err := leg.Seal()
		require.NoError(t, err)
		return sealed
	}
	legOne := seedLeg("b-1", 1, billing.LegOutcomeWinner)
	legTwo := seedLeg("b-2", 2, billing.LegOutcomeFailed)
	applyPG := func(input billing.ProviderCostRevisionInput) billing.ProviderCostRevisionResult {
		t.Helper()
		result, err := store.ApplyProviderCostRevision(ctx, input)
		require.NoError(t, err)
		require.True(t, result.Applied)
		return result
	}
	applyPG(c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-shared", "head-shared-1", 1, 30, true))
	applyPG(c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-2", "charge-shared", "head-shared-2", 1, 20, true))
	for _, leg := range []billing.CallLegUsageRecord{legOne, legTwo} {
		claimed, err := store.ClaimProviderCostWorkForRevision(ctx, billing.ProviderCostWork{AccountID: accountID, CallID: callID, Leg: leg})
		require.NoError(t, err)
		require.True(t, claimed)
	}

	report, err := store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.NoError(t, err)
	byLeg := make(map[string]billing.ALegReportLeg)
	for i := range report.Calls {
		for j := range report.Calls[i].BLegs {
			byLeg[report.Calls[i].BLegs[j].BLegID] = report.Calls[i].BLegs[j]
		}
	}
	require.Equal(t, billing.ALegProviderKnown, byLeg["b-1"].ProviderStatus)
	require.Equal(t, int64(30), byLeg["b-1"].ProviderCost.Nano)
	require.Len(t, byLeg["b-1"].ProviderChildren, 1)
	require.Equal(t, billing.ALegProviderKnown, byLeg["b-2"].ProviderStatus)
	require.Equal(t, int64(20), byLeg["b-2"].ProviderCost.Nano)
	require.Len(t, byLeg["b-2"].ProviderChildren, 1)
	require.Equal(t, int64(50), report.Provider.KnownSubtotal.Nano)
	require.Equal(t, 2, report.Provider.KnownLegs)
	require.Equal(t, 0, report.Provider.UnknownLegs)
}

// TestPostgresProviderNineChildrenFanout proves the total per-B-leg
// loader bound on direct PostgreSQL: nine distinct children exceed
// the eight-child window, so the leg resolves unknown with a fanout
// issue and no partial subtotal.
func TestPostgresProviderNineChildrenFanout(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 8)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const accountID, aLegID = "aleg-pg-ninechild", "a-leg-pg"
	require.NoError(t, store.CreateAccount(ctx, billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 10_000_000, State: billing.AccountReady, Version: 1,
	}))
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	require.NoError(t, store.AppendCallUsage(ctx, billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: accountID, ALegID: aLegID, SessionID: "sess-" + aLegID,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v1"},
	}))
	leg := billing.CallLegUsageRecord{
		CallID: callID, ALegID: aLegID, BLegID: "b-1", AttemptSeq: 1,
		BackendID: "ok", ProviderID: "provider-a", ModelID: "model-a",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
	}
	require.NoError(t, store.AppendCallLegUsage(ctx, leg))
	sealed, err := leg.Seal()
	require.NoError(t, err)
	for i := 0; i < 9; i++ {
		chargeID := fmt.Sprintf("charge-%d", i)
		result, err := store.ApplyProviderCostRevision(ctx, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", chargeID, "head-"+chargeID, 1, 1, true))
		require.NoError(t, err)
		require.True(t, result.Applied)
	}
	claimed, err := store.ClaimProviderCostWorkForRevision(ctx, billing.ProviderCostWork{AccountID: accountID, CallID: callID, Leg: sealed})
	require.NoError(t, err)
	require.True(t, claimed)

	report, err := store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.NoError(t, err)
	var resolved *billing.ALegReportLeg
	for i := range report.Calls {
		for j := range report.Calls[i].BLegs {
			if report.Calls[i].BLegs[j].BLegID == "b-1" {
				resolved = &report.Calls[i].BLegs[j]
			}
		}
	}
	require.NotNil(t, resolved)
	require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus)
	require.Empty(t, resolved.ProviderChildren)
	foundFanout := false
	for _, issue := range report.Issues {
		if issue.Code == "provider_cost_fanout" {
			foundFanout = true
		}
	}
	require.True(t, foundFanout, "overflow must carry explicit fanout info, issues = %+v", report.Issues)
	require.Equal(t, int64(0), report.Provider.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Provider.UnknownLegs)
}

// zero children, and mixed zero/nonzero children through the real
// writer on direct PostgreSQL.
func TestPostgresProviderZeroChildren(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 8)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const accountID, aLegID = "aleg-pg-zero", "a-leg-pg"
	require.NoError(t, store.CreateAccount(ctx, billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 10_000_000, State: billing.AccountReady, Version: 1,
	}))
	seedPGBleg := func(callID billing.BillingCallID, bLegID string, seq int) billing.CallLegUsageRecord {
		t.Helper()
		leg := billing.CallLegUsageRecord{
			CallID: callID, ALegID: aLegID, BLegID: bLegID, AttemptSeq: seq,
			BackendID: "ok", ProviderID: "provider-a", ModelID: "model-a",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
			Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		}
		require.NoError(t, store.AppendCallLegUsage(ctx, leg))
		sealed, err := leg.Seal()
		require.NoError(t, err)
		return sealed
	}
	applyPG := func(input billing.ProviderCostRevisionInput) billing.ProviderCostRevisionResult {
		t.Helper()
		result, err := store.ApplyProviderCostRevision(ctx, input)
		require.NoError(t, err)
		require.True(t, result.Applied)
		return result
	}
	claimPG := func(callID billing.BillingCallID, leg billing.CallLegUsageRecord) {
		t.Helper()
		claimed, err := store.ClaimProviderCostWorkForRevision(ctx, billing.ProviderCostWork{AccountID: accountID, CallID: callID, Leg: leg})
		require.NoError(t, err)
		require.True(t, claimed)
	}
	newPGCall := func() billing.BillingCallID {
		t.Helper()
		callID, err := billing.NewBillingCallID()
		require.NoError(t, err)
		require.NoError(t, store.AppendCallUsage(ctx, billing.CallUsageRecord{
			SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
			AccountID: accountID, ALegID: aLegID, SessionID: "sess-" + aLegID,
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
			Outcome:            billing.TurnOutcomeCompleted,
			CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"},
			ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v1"},
		}))
		return callID
	}
	findPGLeg := func(report billing.ALegReport, callID billing.BillingCallID, bLegID string) billing.ALegReportLeg {
		t.Helper()
		for _, row := range report.Calls {
			if row.CallID != callID {
				continue
			}
			for _, leg := range row.BLegs {
				if leg.BLegID == bLegID {
					return leg
				}
			}
		}
		t.Fatalf("leg %q not on page", bLegID)
		return billing.ALegReportLeg{}
	}

	// One zero child: reversed to zero through a real payer correction.
	callOne := newPGCall()
	legOne := seedPGBleg(callOne, "b-1", 1)
	applyPG(c2r2ChildRevisionInput(t, accountID, callOne, aLegID, "b-1", "charge-a", "head-charge-a", 1, 10, true))
	applyPG(c2r2ChildRevisionInput(t, accountID, callOne, aLegID, "b-1", "charge-a", "head-charge-a", 2, 10, false))
	claimPG(callOne, legOne)

	// Multiple zero children: both reversed to zero.
	callMulti := newPGCall()
	legMulti := seedPGBleg(callMulti, "b-1", 1)
	for _, chargeID := range []string{"charge-a", "charge-b"} {
		applyPG(c2r2ChildRevisionInput(t, accountID, callMulti, aLegID, "b-1", chargeID, "head-"+chargeID, 1, 10, true))
		applyPG(c2r2ChildRevisionInput(t, accountID, callMulti, aLegID, "b-1", chargeID, "head-"+chargeID, 2, 10, false))
	}
	claimPG(callMulti, legMulti)

	// Mixed zero plus nonzero children.
	callMixed := newPGCall()
	legMixed := seedPGBleg(callMixed, "b-1", 1)
	applyPG(c2r2ChildRevisionInput(t, accountID, callMixed, aLegID, "b-1", "charge-a", "head-charge-a", 1, 25, true))
	applyPG(c2r2ChildRevisionInput(t, accountID, callMixed, aLegID, "b-1", "charge-b", "head-charge-b", 1, 10, true))
	applyPG(c2r2ChildRevisionInput(t, accountID, callMixed, aLegID, "b-1", "charge-b", "head-charge-b", 2, 10, false))
	claimPG(callMixed, legMixed)

	report, err := store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.NoError(t, err)

	one := findPGLeg(report, callOne, "b-1")
	require.Equal(t, billing.ALegProviderKnownZero, one.ProviderStatus)
	require.Equal(t, billing.ALegProviderZeroAllChildrenZero, one.ZeroBasis)
	require.Len(t, one.ProviderChildren, 1)

	multi := findPGLeg(report, callMulti, "b-1")
	require.Equal(t, billing.ALegProviderKnownZero, multi.ProviderStatus)
	require.Equal(t, billing.ALegProviderZeroAllChildrenZero, multi.ZeroBasis)
	require.Len(t, multi.ProviderChildren, 2)

	mixed := findPGLeg(report, callMixed, "b-1")
	require.Equal(t, billing.ALegProviderKnown, mixed.ProviderStatus)
	require.Equal(t, int64(25), mixed.ProviderCost.Nano)
	require.Len(t, mixed.ProviderChildren, 2)

	require.Equal(t, int64(25), report.Provider.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Provider.KnownLegs)
	require.Equal(t, 2, report.Provider.ZeroLegs)
	require.Equal(t, 0, report.Provider.UnknownLegs)
}
