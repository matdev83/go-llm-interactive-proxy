package billingstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// These tests certify the durable boundary using valid DTOs, independent of
// rating and catalog availability. Retrieval must return the stored result.
func TestSupportAdvisoryDurableRoundTrip(t *testing.T) {
	runSupportAdvisoryDurableRoundTrip(t, newSQLiteTestStore)
}

func runSupportAdvisoryDurableRoundTrip(t *testing.T, open func(t *testing.T) *DurableStore) {
	t.Helper()
	for _, state := range []string{"complete", "failing", "clean-enabled", "incomplete", "historical"} {
		t.Run(state, func(t *testing.T) {
			store := open(t)
			ctx := context.Background()
			edTestAccount(t, store, "account-advice", "USD")
			callID := edTestCallID(t)
			subject := edTestBLegSubject(store.StoreID(), "tenant-ed", "account-advice", "a-advice", callID.String(), "b-advice")
			observation := edTestObservation(t, "obs-advice", metering.OriginProvider, "stream-advice", 1, subject,
				[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "5")}, nil)
			edSetupCall(t, store, subject.AccountID, callID, subject.ALegID, edTestLeg(t, subject.BLegID, observation))
			valuation := phase4EconomicsValuation(t, observation, "val-advice", time.Unix(1_700_020_100, 0).UTC(), "")
			advisoryStoreAttach(&valuation, state)
			require.NoError(t, valuation.Validate())
			frozen := valuation.Clone()
			payload, err := frozen.CanonicalJSON()
			require.NoError(t, err)
			require.NoError(t, store.AppendValuation(ctx, valuation))
			require.NoError(t, store.AppendValuation(ctx, frozen), "identical enabled replay")

			// Mutating caller-owned slices after append cannot rewrite the durable row.
			advisoryStoreMutate(&valuation)
			got, err := store.GetValuation(ctx, frozen.ID, frozen.Version)
			require.NoError(t, err)
			advisoryStoreAssert(t, got, state)
			gotPayload, err := got.CanonicalJSON()
			require.NoError(t, err)
			require.Equal(t, string(payload), string(gotPayload))
			require.Equal(t, frozen.Fingerprint(), got.Fingerprint())
			require.Equal(t, frozen.ContextHash(), got.ContextHash())
			require.Equal(t, frozen.InputSetHash, got.InputSetHash)

			page, err := store.ListValuations(ctx, ValuationQuery{Subject: &subject})
			require.NoError(t, err)
			require.Len(t, page.Valuations, 1)
			advisoryStoreAssert(t, page.Valuations[0], state)
			detail, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
				StoreID: store.StoreID(), TenantID: subject.TenantID, AccountID: subject.AccountID, BillingCallID: callID.String(),
			})
			require.NoError(t, err)
			require.Len(t, detail.Valuations, 1)
			advisoryStoreAssert(t, detail.Valuations[0], state)

			// Clone and each query result own the report, nested component dimensions,
			// context slice and incompleteness slice; no consumer can mutate another.
			cloned := got.Clone()
			advisoryStoreMutate(&cloned)
			advisoryStoreAssert(t, got, state)
			advisoryStoreMutate(&got)
			advisoryStoreAssert(t, page.Valuations[0], state)
			advisoryStoreMutate(&page.Valuations[0])
			advisoryStoreAssert(t, detail.Valuations[0], state)
			advisoryStoreMutate(&detail.Valuations[0])
			fresh, err := store.GetValuation(ctx, frozen.ID, frozen.Version)
			require.NoError(t, err)
			advisoryStoreAssert(t, fresh, state)
		})
	}
}

func advisoryStoreAttach(v *economics.Valuation, state string) {
	if state == "historical" {
		return
	}
	c := economics.SupportAdvisoryContext{
		Version:       economics.SupportAdvisoryVersionV1,
		Tariff:        economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "advice-tariff", Version: "v1"}, RaterID: "advice-rater"},
		TariffContent: economics.SnapshotContentRef{ContentRef: "catalog://advice-tariff/v1", ContentHash: strings.Repeat("a", 64)},
	}
	v.SupportAdvisoryContexts = []economics.SupportAdvisoryContext{c}
	if state == "clean-enabled" {
		return
	}
	left := metering.ComponentKey{Direction: metering.DirectionInput, Component: "vendor:left", Unit: metering.UnitToken, SchemaID: "vendor:support:v1", Dimensions: []metering.Dimension{{Name: "region", Value: "eu"}}}
	right := left.Clone()
	right.Component = "vendor:right"
	v.SupportAdvisory = &economics.SupportAdvisoryReport{Pairs: []economics.SupportAdvisoryPair{{ContextKey: c.Key(), ScopeKey: "scope-advice", Left: left, Right: right}}}
	if state == "incomplete" {
		v.SupportAdvisory.IncompleteContexts = []economics.SupportAdvisoryIncomplete{{ContextKey: c.Key(), Reason: economics.SupportAdvisoryGraphBudget}}
	}
	if state == "failing" {
		v.Completeness = economics.CompletenessConflict
	}
}

func advisoryStoreMutate(v *economics.Valuation) {
	if len(v.SupportAdvisoryContexts) != 0 {
		v.SupportAdvisoryContexts[0].TariffContent.ContentRef = "mutated"
	}
	if v.SupportAdvisory != nil {
		if len(v.SupportAdvisory.Pairs) != 0 {
			v.SupportAdvisory.Pairs[0].ScopeKey = "mutated"
			v.SupportAdvisory.Pairs[0].Left.Dimensions[0].Value = "mutated"
			v.SupportAdvisory.Pairs[0].Right.Dimensions[0].Value = "mutated"
		}
		if len(v.SupportAdvisory.IncompleteContexts) != 0 {
			v.SupportAdvisory.IncompleteContexts[0].Reason = economics.SupportAdvisoryPairLimit
		}
	}
}

func advisoryStoreAssert(t *testing.T, v economics.Valuation, state string) {
	t.Helper()
	wantCompleteness := economics.CompletenessComplete
	if state == "failing" {
		wantCompleteness = economics.CompletenessConflict
	}
	require.Equal(t, wantCompleteness, v.Completeness)
	require.Len(t, v.Lines, 1)
	require.Equal(t, metering.Decimal{Coefficient: "5", Scale: 0}, *v.Lines[0].Quantity)
	require.Equal(t, metering.Decimal{Coefficient: "25", Scale: 3}, *v.Lines[0].UnitPrice)
	require.Equal(t, metering.Decimal{Coefficient: "125", Scale: 3}, *v.Lines[0].Amount)
	require.Equal(t, economics.Money{Currency: "USD", NanoUnits: 125_000_000, Present: true}, *v.Lines[0].RoundedAmount)
	require.Len(t, v.Totals, 1)
	require.Equal(t, metering.Decimal{Coefficient: "125", Scale: 3}, *v.Totals[0].Amount)
	require.Equal(t, economics.Money{Currency: "USD", NanoUnits: 125_000_000, Present: true}, v.Totals[0].RoundedAmount)
	if state == "historical" {
		require.Nil(t, v.SupportAdvisoryContexts)
		require.Nil(t, v.SupportAdvisory)
		return
	}
	require.Len(t, v.SupportAdvisoryContexts, 1)
	c := v.SupportAdvisoryContexts[0]
	require.Equal(t, economics.SupportAdvisoryVersionV1, c.Version)
	require.Equal(t, "advice-tariff", c.Tariff.ID)
	require.Equal(t, "v1", c.Tariff.Version)
	require.Equal(t, "advice-rater", c.Tariff.RaterID)
	require.Equal(t, "catalog://advice-tariff/v1", c.TariffContent.ContentRef)
	require.Equal(t, strings.Repeat("a", 64), c.TariffContent.ContentHash)
	const key = `["component-support-advisory-v1","advice-tariff","v1","advice-rater","catalog://advice-tariff/v1","aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]`
	require.Equal(t, key, c.Key())
	if state == "clean-enabled" {
		require.Nil(t, v.SupportAdvisory)
		return
	}
	require.NotNil(t, v.SupportAdvisory)
	require.Len(t, v.SupportAdvisory.Pairs, 1)
	p := v.SupportAdvisory.Pairs[0]
	require.Equal(t, key, p.ContextKey)
	require.Equal(t, "scope-advice", p.ScopeKey)
	left := metering.ComponentKey{Direction: metering.DirectionInput, Component: "vendor:left", Unit: metering.UnitToken, SchemaID: "vendor:support:v1", Dimensions: []metering.Dimension{{Name: "region", Value: "eu"}}}
	right := left.Clone()
	right.Component = "vendor:right"
	require.Equal(t, left, p.Left)
	require.Equal(t, right, p.Right)
	if state == "incomplete" {
		require.Equal(t, []economics.SupportAdvisoryIncomplete{{ContextKey: key, Reason: economics.SupportAdvisoryGraphBudget}}, v.SupportAdvisory.IncompleteContexts)
	} else {
		require.Empty(t, v.SupportAdvisory.IncompleteContexts)
	}
}

func TestSupportAdvisoryDurableReplayConflict(t *testing.T) {
	runSupportAdvisoryDurableReplayConflict(t, newSQLiteTestStore)
}

func runSupportAdvisoryDurableReplayConflict(t *testing.T, open func(t *testing.T) *DurableStore) {
	t.Helper()
	store := open(t)
	ctx := context.Background()
	observation := phase4EconomicsObservation(store.StoreID(), "obs-replay-advice", 1)
	original := phase4EconomicsValuation(t, observation, "val-replay-advice", time.Unix(1_700_020_100, 0).UTC(), "")
	advisoryStoreAttach(&original, "incomplete")
	originalPayload, err := original.CanonicalJSON()
	require.NoError(t, err)
	require.NoError(t, store.AppendValuation(ctx, original))
	require.NoError(t, store.AppendValuation(ctx, original.Clone()))
	for _, newID := range []string{original.ID, "val-composite-report-divergence"} {
		changed := original.Clone()
		changed.SupportAdvisory.IncompleteContexts[0].Reason = economics.SupportAdvisoryCandidateBudget
		changed.ID = newID
		require.NoError(t, changed.Validate())
		require.Equal(t, original.ContextHash(), changed.ContextHash(), "report is outside interpretation identity")
		require.Equal(t, original.InputSetHash, changed.InputSetHash)
		require.NotEqual(t, original.Fingerprint(), changed.Fingerprint())
		require.ErrorIs(t, store.AppendValuation(ctx, changed), ErrIdentityConflict,
			"a composed report may change the primary ID; interpretation uniqueness must still reject it")
	}
	page, err := store.ListValuations(ctx, ValuationQuery{Subject: &original.Subject})
	require.NoError(t, err)
	require.Len(t, page.Valuations, 1)
	advisoryStoreAssert(t, page.Valuations[0], "incomplete")
	require.Equal(t, original.Fingerprint(), page.Valuations[0].Fingerprint())
	gotPayload, err := page.Valuations[0].CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, string(originalPayload), string(gotPayload), "changed report retries must leave the original durable row byte-identical")
}

func TestSupportAdvisoryHistoricalRowExactReplay(t *testing.T) {
	runSupportAdvisoryHistoricalRowExactReplay(t, newSQLiteTestStore)
}

func runSupportAdvisoryHistoricalRowExactReplay(t *testing.T, open func(t *testing.T) *DurableStore) {
	t.Helper()
	// Literal pre-advisory wire fixture, with no input hash or reporting fields.
	const payload = `{"id":"historical-advice","version":2,"perspective":"operator","basis":"provider_reported","subject":{"kind":"b_leg","store_id":"test","b_leg_id":"b-historical"},"scope":"legacy-scope","input_observations":[{"store_id":"test","observation_id":"obs-historical","revision":1,"payload_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],"rater":{"id":"","version":""},"tariff":{"id":"","version":""},"policy":{"id":"","version":""},"lines":null,"totals":null,"completeness":"partial","created_at":"2025-01-02T03:04:05Z"}`
	const fingerprint = "ba0c32dfe07d2abaa91a40db12aa3fbd3e758625b377f9da36228f97b7e71a02"
	store := open(t)
	var historical economics.Valuation
	require.NoError(t, json.Unmarshal([]byte(payload), &historical))
	insertRepair7LegacyValuation(t, store, historical, payload, fingerprint, BillingEconomicsProjectionVersion)
	got, err := store.GetValuation(context.Background(), historical.ID, historical.Version)
	require.NoError(t, err)
	require.Nil(t, got.SupportAdvisoryContexts)
	require.Nil(t, got.SupportAdvisory)
	require.Empty(t, got.InputSetHash)
	require.Equal(t, fingerprint, got.Fingerprint())
	require.NoError(t, store.AppendValuation(context.Background(), got))
	var stored, storedFingerprint, inputHash string
	require.NoError(t, store.db.NewRaw(`SELECT canonical_json, fingerprint, input_set_hash FROM billing_valuations WHERE store_id = ? AND valuation_id = ?`, store.StoreID(), historical.ID).Scan(context.Background(), &stored, &storedFingerprint, &inputHash))
	require.Equal(t, payload, stored)
	require.Equal(t, fingerprint, storedFingerprint)
	require.Empty(t, inputHash)
	require.Equal(t, historical.ID, got.ID)
	require.Equal(t, historical.ContextHash(), got.ContextHash())
}

func runSupportAdvisoryDurableParity(t *testing.T, open func(t *testing.T) *DurableStore) {
	t.Helper()
	t.Run("RoundTripAndQueries", func(t *testing.T) {
		runSupportAdvisoryDurableRoundTrip(t, open)
	})
	t.Run("ChangedReportReplay", func(t *testing.T) {
		runSupportAdvisoryDurableReplayConflict(t, open)
	})
	t.Run("HistoricalExactReplay", func(t *testing.T) {
		runSupportAdvisoryHistoricalRowExactReplay(t, open)
	})
	t.Run("AppendRollbackAndRetry", func(t *testing.T) {
		runSupportAdvisoryAppendRollback(t, open)
	})
	t.Run("SettlementFence", func(t *testing.T) {
		runSupportAdvisorySettlementParity(t, open)
	})
}

func runSupportAdvisoryAppendRollback(t *testing.T, open func(t *testing.T) *DurableStore) {
	t.Helper()
	store := open(t)
	ctx := context.Background()
	edTestAccount(t, store, "account-advice-rollback", "USD")
	callID := edTestCallID(t)
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", "account-advice-rollback", "a-advice-rollback", callID.String(), "b-advice-rollback")
	observation := edTestObservation(t, "obs-advice-rollback", metering.OriginProvider, "stream-advice-rollback", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "5")}, nil)
	edSetupCall(t, store, subject.AccountID, callID, subject.ALegID, edTestLeg(t, subject.BLegID, observation))
	valuation := phase4EconomicsValuation(t, observation, "val-advice-rollback", time.Unix(1_700_020_100, 0).UTC(), "")
	advisoryStoreAttach(&valuation, "incomplete")
	wantPayload, err := valuation.CanonicalJSON()
	require.NoError(t, err)
	fault := errors.New("injected valuation append failure")
	store.SetEconomicFaultHook(func(stage string) error {
		if stage == "after_valuation" {
			return fault
		}
		return nil
	})
	require.ErrorIs(t, store.AppendValuation(ctx, valuation), fault)
	store.SetEconomicFaultHook(nil)

	assertNoAdvisoryValuationRows(ctx, t, store)
	page, err := store.ListValuations(ctx, ValuationQuery{Subject: &subject})
	require.NoError(t, err)
	require.Empty(t, page.Valuations, "failed transaction must not expose a partial report")
	detail, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), TenantID: subject.TenantID, AccountID: subject.AccountID, BillingCallID: callID.String(),
	})
	require.NoError(t, err)
	require.Empty(t, detail.Valuations, "failed transaction must not leak into the existing detail query")

	// The failed append left no identity or projection behind, so the exact same
	// immutable report can be retried and becomes visible through every query.
	require.NoError(t, store.AppendValuation(ctx, valuation))
	assertOneAdvisoryValuationRows(ctx, t, store)
	got, err := store.GetValuation(ctx, valuation.ID, valuation.Version)
	require.NoError(t, err)
	advisoryStoreAssert(t, got, "incomplete")
	gotPayload, err := got.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, string(wantPayload), string(gotPayload))
	page, err = store.ListValuations(ctx, ValuationQuery{Subject: &subject})
	require.NoError(t, err)
	require.Len(t, page.Valuations, 1)
	advisoryStoreAssert(t, page.Valuations[0], "incomplete")
	detail, err = store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), TenantID: subject.TenantID, AccountID: subject.AccountID, BillingCallID: callID.String(),
	})
	require.NoError(t, err)
	require.Len(t, detail.Valuations, 1)
	advisoryStoreAssert(t, detail.Valuations[0], "incomplete")
}

func assertNoAdvisoryValuationRows(ctx context.Context, t *testing.T, store *DurableStore) {
	t.Helper()
	for _, table := range []string{"billing_valuations", "billing_valuation_lines"} {
		var count int
		require.NoError(t, store.db.NewRaw("SELECT COUNT(1) FROM "+table+" WHERE store_id = ?", store.StoreID()).Scan(ctx, &count))
		require.Zero(t, count, "%s rows leaked from failed append", table)
	}
}

func assertOneAdvisoryValuationRows(ctx context.Context, t *testing.T, store *DurableStore) {
	t.Helper()
	for _, table := range []string{"billing_valuations", "billing_valuation_lines"} {
		var count int
		require.NoError(t, store.db.NewRaw("SELECT COUNT(1) FROM "+table+" WHERE store_id = ?", store.StoreID()).Scan(ctx, &count))
		require.Equal(t, 1, count, "%s row count after retry", table)
	}
}

func runSupportAdvisorySettlementParity(t *testing.T, open func(t *testing.T) *DurableStore) {
	t.Helper()
	store := open(t)
	f3ActivateEmpty(t, store)

	settleUnknown := func(label, scenario, accountID, bLegID, tariffVersion, reportingVersion string, wantNano int64) billing.Money {
		parent := f356Key("vendor:advice_" + scenario + "_parent")
		left := f356Key("vendor:advice_" + scenario + "_left")
		right := f356Key("vendor:advice_" + scenario + "_right")
		rules := []economics.RatingRule{
			f356LinearRule(t, "advice-"+scenario+"-free-parent", parent, "0"),
			f356LinearRule(t, "advice-"+scenario+"-left", left, "1"),
			f356LinearRule(t, "advice-"+scenario+"-right", right, "1"),
		}
		relationships := []metering.ComponentRelationship{
			{Kind: metering.RelationshipSubset, Parent: parent, Child: left},
			{Kind: metering.RelationshipSubset, Parent: parent, Child: right},
		}
		tariff := f356TariffWithReportingVersion(t, tariffVersion, reportingVersion, rules, relationships)
		policy := ref83intWinnerPolicy()
		policy.PricingRef.Version = tariffVersion
		call, exposure, rated, rateErr := f356SetupOnActivatedStore(t, store, accountID, bLegID, policy, tariff,
			f356M(parent, "100"), f356M(left, "20"), f356M(right, "30"))
		require.NoError(t, rateErr)
		require.Equal(t, economics.CompletenessComplete, rated.CustomerValuation.Completeness)
		require.Equal(t, billing.Money{Nano: wantNano, Currency: "USD"}, rated.CustomerCharge)
		if reportingVersion == "" {
			require.Nil(t, rated.CustomerValuation.SupportAdvisoryContexts)
			require.Nil(t, rated.CustomerValuation.SupportAdvisory)
		} else {
			require.Len(t, rated.CustomerValuation.SupportAdvisoryContexts, 1)
			require.Equal(t, economics.SupportAdvisoryVersionV1, rated.CustomerValuation.SupportAdvisoryContexts[0].Version)
			require.Equal(t, tariffVersion, rated.CustomerValuation.SupportAdvisoryContexts[0].Tariff.Version)
			require.NotNil(t, rated.CustomerValuation.SupportAdvisory)
			require.Len(t, rated.CustomerValuation.SupportAdvisory.Pairs, 1)
			pair := rated.CustomerValuation.SupportAdvisory.Pairs[0]
			components := []string{pair.Left.Component, pair.Right.Component}
			sort.Strings(components)
			require.Equal(t, []string{left.Component, right.Component}, components)
			require.Empty(t, rated.CustomerValuation.SupportAdvisory.IncompleteContexts)
		}
		f356RequireSettlesOnce(t, store, call, exposure, rated, wantNano, label)
		return rated.CustomerCharge
	}

	legacyUnknown := settleUnknown("unknown_legacy", "unknown", "advice-unknown-legacy", "b-advice-unknown-legacy", "v3", "", 50_000_000_000)
	v1Unknown := settleUnknown("unknown_v1", "unknown", "advice-unknown-v1", "b-advice-unknown-v1", "v4", economics.SupportAdvisoryVersionV1, 50_000_000_000)
	require.Equal(t, legacyUnknown, v1Unknown, "enabling unknown-pair reporting must preserve the 50 USD debit")

	settleLimited := func(label, scenario, accountID, bLegID, tariffVersion, reportingVersion string) billing.Money {
		// Eighteen paid siblings admit 18*17/2 = 153 unordered unknown
		// pairs; v1 retains the published 128-entry prefix and marks pair_limit.
		parent := f356Key("vendor:advice_" + scenario + "_parent")
		rules := []economics.RatingRule{f356LinearRule(t, "advice-"+scenario+"-free-parent", parent, "0")}
		relationships := make([]metering.ComponentRelationship, 0, 18)
		measures := make([]f356Measure, 0, 19)
		measures = append(measures, f356M(parent, "100"))
		for i := 0; i < 18; i++ {
			child := f356Key(fmt.Sprintf("vendor:advice_%s_child_%02d", scenario, i))
			relationships = append(relationships, metering.ComponentRelationship{Kind: metering.RelationshipSubset, Parent: parent, Child: child})
			rules = append(rules, f356LinearRule(t, fmt.Sprintf("advice-%s-child-%02d", scenario, i), child, "1"))
			measures = append(measures, f356M(child, "1"))
		}
		tariff := f356TariffWithReportingVersion(t, tariffVersion, reportingVersion, rules, relationships)
		policy := ref83intWinnerPolicy()
		policy.PricingRef.Version = tariffVersion
		call, exposure, rated, rateErr := f356SetupOnActivatedStore(t, store, accountID, bLegID, policy, tariff, measures...)
		require.NoError(t, rateErr)
		require.Equal(t, economics.CompletenessComplete, rated.CustomerValuation.Completeness)
		require.Equal(t, billing.Money{Nano: 18_000_000_000, Currency: "USD"}, rated.CustomerCharge)
		if reportingVersion == "" {
			require.Nil(t, rated.CustomerValuation.SupportAdvisoryContexts)
			require.Nil(t, rated.CustomerValuation.SupportAdvisory)
		} else {
			require.Len(t, rated.CustomerValuation.SupportAdvisoryContexts, 1)
			require.NotNil(t, rated.CustomerValuation.SupportAdvisory)
			require.Len(t, rated.CustomerValuation.SupportAdvisory.Pairs, 128)
			require.Equal(t, []economics.SupportAdvisoryIncomplete{{
				ContextKey: rated.CustomerValuation.SupportAdvisoryContexts[0].Key(),
				Reason:     economics.SupportAdvisoryPairLimit,
			}}, rated.CustomerValuation.SupportAdvisory.IncompleteContexts)
		}
		f356RequireSettlesOnce(t, store, call, exposure, rated, 18_000_000_000, label)
		return rated.CustomerCharge
	}

	legacyLimited := settleLimited("limited_legacy", "limited", "advice-limited-legacy", "b-advice-limited-legacy", "v5", "")
	v1Limited := settleLimited("limited_v1", "limited", "advice-limited-v1", "b-advice-limited-v1", "v6", economics.SupportAdvisoryVersionV1)
	require.Equal(t, legacyLimited, v1Limited, "pair-limit incompleteness must preserve the 18 USD debit")

	// Existing paid ancestor/descendant conflicts remain incomplete under the
	// enabled reporting version and cannot cross the V2 settlement fence.
	parent := f356Key("vendor:advice_conflict_parent")
	child := f356Key("vendor:advice_conflict_child")
	tariff := f356TariffWithReportingVersion(t, "v7", economics.SupportAdvisoryVersionV1,
		[]economics.RatingRule{
			f356LinearRule(t, "advice-conflict-parent", parent, "1"),
			f356LinearRule(t, "advice-conflict-child", child, "1"),
		}, []metering.ComponentRelationship{{Kind: metering.RelationshipSubset, Parent: parent, Child: child}})
	policy := ref83intWinnerPolicy()
	policy.PricingRef.Version = "v7"
	call, exposure, rated, rateErr := f356SetupOnActivatedStore(t, store, "advice-conflict", "b-advice-conflict", policy, tariff,
		f356M(parent, "100"), f356M(child, "40"))
	require.ErrorIs(t, rateErr, billing.ErrSchemaOverlapConflict)
	require.NotEqual(t, economics.CompletenessComplete, rated.CustomerValuation.Completeness)
	require.Len(t, rated.CustomerValuation.SupportAdvisoryContexts, 1)
	adversarial := f356NormalizedCharge(rated)
	f356RequireCompletenessFence(t, billing.ValidateCallRatingResultForSettlement(adversarial, call, exposure, billing.PostingOwnerV2),
		string(rated.CustomerValuation.Completeness))
	before := f356SnapshotOf(t, store, call.AccountID, call.CallID)
	f356RequirePinnedOpenV2(t, before)
	_, applyErr := store.ApplyCallBillingResult(context.Background(), billing.ApplyCallBillingInput{
		Call: call, Exposure: exposure, Result: adversarial, PostingOwner: billing.PostingOwnerV2,
	})
	f356RequireCompletenessFence(t, applyErr, string(rated.CustomerValuation.Completeness))
	after := f356SnapshotOf(t, store, call.AccountID, call.CallID)
	require.True(t, f356SameSnapshot(before, after), "known conflict must leave balance, journal, exposure and pin unchanged")
}
