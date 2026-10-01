package billing_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestUncertaintyLegacyMoneyAndKnownConflictControls(t *testing.T) {
	t.Parallel()

	// Vector 29 is the legacy unversioned 100/20/30 subset-sibling baseline:
	// additive charge remains complete at 50 with no error. The other vectors
	// keep representative pre-existing overlap, quantity, cover and ambiguity
	// failures typed while advisory output is absent.
	wanted := map[int]struct{}{1: {}, 10: {}, 19: {}, 20: {}, 29: {}}
	for _, vector := range accVectors() {
		if _, ok := wanted[vector.number]; !ok {
			continue
		}
		vector := vector
		delete(wanted, vector.number)
		t.Run(accNumber(vector.number)+"_"+vector.name, func(t *testing.T) {
			t.Parallel()
			testCase := vector.build(t)
			resolved := f356Schema(t, "uncertainty-legacy-"+accNumber(vector.number), testCase.rules, testCase.relationships)
			accCheckSeams(t, vector, resolved, testCase.observations)
		})
	}
	if len(wanted) != 0 {
		t.Fatalf("missing baseline acceptance vectors: %v", wanted)
	}
}

func TestUncertaintyLegacyBranchRootAdvisoryTextIsPinned(t *testing.T) {
	t.Parallel()

	root := r7Key("vendor:uncertainty_branch_root")
	branchB := r7Key("vendor:uncertainty_branch_b")
	branchC := r7Key("vendor:uncertainty_branch_c")
	contradictionY := r7Key("vendor:uncertainty_contradiction_y")
	contradictionX := r7Key("vendor:uncertainty_contradiction_x")
	relationships := []metering.ComponentRelationship{
		{Kind: metering.RelationshipPartition, Parent: root, Child: branchB},
		{Kind: metering.RelationshipPartition, Parent: root, Child: branchC},
		{Kind: metering.RelationshipSubset, Parent: contradictionY, Child: contradictionX},
	}
	rules := []economics.RatingRule{
		b1Rule(t, "legacy-branch-b-rate", branchB, "1"),
		b1Rule(t, "legacy-branch-c-rate", branchC, "1"),
		b1Rule(t, "legacy-contradiction-y-rate", contradictionY, "1"),
		b1Rule(t, "legacy-contradiction-x-rate", contradictionX, "1"),
	}
	resolved := f356Schema(t, "uncertainty-legacy-branch-root", rules, relationships)
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	observation := b1Observation(t, "uncertainty-legacy-branch-root", "b-leg-uncertainty-legacy",
		metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
		b1Measure(t, root, "100"), b1Measure(t, branchB, "40"), b1Measure(t, branchC, "60"),
		b1Measure(t, contradictionY, "10"), b1Measure(t, contradictionX, "20"))
	valuation, err := rater.Rate(context.Background(), b1OperatorInput(t, resolved, []metering.Observation{observation}))
	if !errors.Is(err, billing.ErrSchemaOverlapConflict) {
		t.Fatalf("legacy result error = %v, want typed overlap conflict", err)
	}
	if valuation.Completeness != economics.CompletenessConflict {
		t.Fatalf("legacy completeness = %q, want conflict", valuation.Completeness)
	}

	// These wire-shaped keys and the scope are hand-authored from this fixture's
	// component identities and fixed b1Observation fields. The formatter below
	// mirrors the documented overlap and legacy unknown-containment templates;
	// neither expected diagnostic is obtained from a production error value.
	// legacyScopeKey is hand-framed in Scope.Key field order: store-b1, empty
	// tenant/account, local origin/acquisition, operator/backend-ingress/backend-
	// attempt, the fixed b-leg lineage JSON, the fixture stream ID, and empty
	// charge scope.
	const (
		branchBJSON        = `{"direction":"input","component":"vendor:uncertainty_branch_b","unit":"token","schema_id":"b1:frozen-overlap:v1"}`
		branchCJSON        = `{"direction":"input","component":"vendor:uncertainty_branch_c","unit":"token","schema_id":"b1:frozen-overlap:v1"}`
		contradictionYJSON = `{"direction":"input","component":"vendor:uncertainty_contradiction_y","unit":"token","schema_id":"b1:frozen-overlap:v1"}`
		contradictionXJSON = `{"direction":"input","component":"vendor:uncertainty_contradiction_x","unit":"token","schema_id":"b1:frozen-overlap:v1"}`
		legacyScopeKey     = `8:store-b10:0:5:local27:local_transport_measurement8:operator15:backend_ingress15:backend_attempt134:{"subject":{"kind":"b_leg","store_id":"store-b1","a_leg_id":"a-b1","billing_call_id":"call-b1","b_leg_id":"b-leg-uncertainty-legacy"}}37:uncertainty-legacy-branch-root-stream0:`
	)
	typedOverlap := fmt.Sprintf(
		"billing: frozen schema component overlap is not payable: payable component %s transitively includes payable component %s in one %q direction %q unit scope",
		contradictionYJSON, contradictionXJSON, "input", "token",
	)
	legacyUnknown := fmt.Sprintf("billing: unknown containment intersection: %s ~ %s in scope %q", branchBJSON, branchCJSON, legacyScopeKey)
	wantDiagnostic := typedOverlap + "\n" + legacyUnknown
	if got := err.Error(); got != wantDiagnostic {
		t.Fatalf("legacy branch-root diagnostic changed:\n got: %s\nwant: %s", got, wantDiagnostic)
	}
}

func TestUncertaintyV1LocalSiblingReportsWithoutChangingMoney(t *testing.T) {
	t.Parallel()
	assertUncertaintyV1Acceptance29(t, economics.BasisLocalExpected)
}

func TestUncertaintyV1ProviderQuantityAndRetailSiblingReports(t *testing.T) {
	t.Parallel()
	for _, basis := range []economics.ValuationBasis{economics.BasisProviderQuantityLocal, economics.BasisCustomerPolicy} {
		basis := basis
		t.Run(string(basis), func(t *testing.T) {
			t.Parallel()
			assertUncertaintyV1Acceptance29(t, basis)
		})
	}
}

func assertUncertaintyV1Acceptance29(t *testing.T, basis economics.ValuationBasis) {
	t.Helper()
	var fixture accCase
	for _, vector := range accVectors() {
		if vector.number == 29 {
			fixture = vector.build(t)
			break
		}
	}
	if fixture.observations == nil {
		t.Fatal("acceptance vector 29 is missing")
	}
	observations := append([]metering.Observation(nil), fixture.observations...)
	if basis == economics.BasisProviderQuantityLocal {
		observations[0].Origin = metering.OriginProvider
		observations[0].Acquisition = metering.AcquisitionProviderResponse
		observations[0].Authority = metering.AuthorityObservedClaim
		observations[0].Lifecycle = metering.LifecycleBackendAttempt
	}
	inputFor := func(snapshot economics.TariffSnapshot) economics.PostUsageRatingInput {
		if basis == economics.BasisCustomerPolicy {
			return b1RetailInput(t, snapshot, observations)
		}
		input := b1OperatorInput(t, snapshot, observations)
		input.Basis = basis
		return input
	}

	legacySnapshot := f356Schema(t, "uncertainty-v1-acceptance-29-"+string(basis), fixture.rules, fixture.relationships)
	legacyRater, err := billing.NewReferenceRater(legacySnapshot)
	if err != nil {
		t.Fatalf("NewReferenceRater legacy: %v", err)
	}
	legacy, legacyErr := legacyRater.Rate(context.Background(), inputFor(legacySnapshot))
	if legacyErr != nil {
		t.Fatalf("legacy rating: %v", legacyErr)
	}
	if legacy.Completeness != economics.CompletenessComplete || smInspect(legacy).total != "50/0" {
		t.Fatalf("legacy control completeness=%q total=%s, want complete 50/0", legacy.Completeness, smInspect(legacy).total)
	}
	if len(legacy.SupportAdvisoryContexts) != 0 || legacy.SupportAdvisory != nil {
		t.Fatalf("unversioned legacy valuation gained advisory fields: contexts=%+v report=%+v", legacy.SupportAdvisoryContexts, legacy.SupportAdvisory)
	}

	enabledSnapshot := legacySnapshot
	enabledSnapshot = uncertaintyV1Snapshot(t, enabledSnapshot)
	enabledRater, err := billing.NewReferenceRater(enabledSnapshot)
	if err != nil {
		t.Fatalf("NewReferenceRater v1: %v", err)
	}
	got, gotErr := enabledRater.Rate(context.Background(), inputFor(enabledSnapshot))
	if gotErr != nil {
		t.Fatalf("v1 rating: %v", gotErr)
	}
	if got.Completeness != economics.CompletenessComplete || smInspect(got).total != "50/0" {
		t.Fatalf("v1 completeness=%q total=%s, want complete 50/0", got.Completeness, smInspect(got).total)
	}
	if !reflect.DeepEqual(got.Lines, legacy.Lines) || !reflect.DeepEqual(got.Totals, legacy.Totals) {
		t.Fatalf("v1 monetary lines/totals differ from the independently pinned legacy vector:\n v1 lines=%+v totals=%+v\nlegacy lines=%+v totals=%+v", got.Lines, got.Totals, legacy.Lines, legacy.Totals)
	}
	if got.ID == legacy.ID {
		t.Fatal("v1 valuation identity did not bind the enabled source context")
	}
	wantContext := economics.SupportAdvisoryContext{
		Version:       economics.SupportAdvisoryVersionV1,
		Tariff:        enabledSnapshot.Ref,
		TariffContent: enabledSnapshot.Content,
	}
	if !reflect.DeepEqual(got.SupportAdvisoryContexts, []economics.SupportAdvisoryContext{wantContext}) {
		t.Fatalf("v1 source contexts=%+v, want exactly %+v", got.SupportAdvisoryContexts, []economics.SupportAdvisoryContext{wantContext})
	}
	if got.SupportAdvisory == nil || len(got.SupportAdvisory.Pairs) != 1 || len(got.SupportAdvisory.IncompleteContexts) != 0 {
		t.Fatalf("v1 report=%+v, want one complete unknown sibling pair", got.SupportAdvisory)
	}
	pair := got.SupportAdvisory.Pairs[0]
	if pair.ContextKey != wantContext.Key() || pair.ScopeKey == "" ||
		pair.Left.CanonicalKey() != accKey("q_leaf_c").CanonicalKey() ||
		pair.Right.CanonicalKey() != accKey("q_leaf_d").CanonicalKey() {
		t.Fatalf("v1 pair=%+v, want source context and the two independently pinned sibling identities", pair)
	}
	contextHash := got.ContextHash()
	linesBefore := append([]economics.LineItem(nil), got.Lines...)
	totalsBefore := append([]economics.CurrencyTotal(nil), got.Totals...)
	changedReport := got.SupportAdvisory.Clone()
	changedReport.Pairs[0].ScopeKey += "-report-only-change"
	got.SupportAdvisory = changedReport
	if got.ContextHash() != contextHash || !reflect.DeepEqual(got.Lines, linesBefore) || !reflect.DeepEqual(got.Totals, totalsBefore) {
		t.Fatal("report-only variation changed the frozen interpretation identity or monetary output")
	}
}

func uncertaintyV1Snapshot(t *testing.T, snapshot economics.TariffSnapshot) economics.TariffSnapshot {
	t.Helper()
	snapshot.SupportAdvisoryVersion = economics.SupportAdvisoryVersionV1
	snapshot.Content = economics.SnapshotContentRef{}
	versioned, err := snapshot.Canonical()
	if err != nil {
		t.Fatalf("canonical v1 snapshot: %v", err)
	}
	return versioned
}

func TestUncertaintyV1EmptySchemaAndContributorlessResultsKeepSourceContext(t *testing.T) {
	t.Parallel()
	fixture := uncertaintyAcceptance29(t)
	for _, tc := range []struct {
		name          string
		rules         []economics.RatingRule
		relationships []metering.ComponentRelationship
		observations  []metering.Observation
		wantTotal     string
	}{
		{
			name:         "empty schema with payable siblings is clean",
			rules:        fixture.rules,
			observations: fixture.observations,
			wantTotal:    "50/0",
		},
		{
			name:          "free only line has no contributor",
			rules:         fixture.rules,
			relationships: fixture.relationships,
			observations:  []metering.Observation{accObs(t, "uncertainty-v1-free-only", "b-leg-free-only", b1Measure(t, *fixture.rules[0].Component, "100"))},
			wantTotal:     "0/0",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			snapshot := uncertaintyV1Snapshot(t, f356Schema(t, "uncertainty-v1-clean-"+strings.ReplaceAll(tc.name, " ", "-"), tc.rules, tc.relationships))
			rater, err := billing.NewReferenceRater(snapshot)
			if err != nil {
				t.Fatalf("NewReferenceRater: %v", err)
			}
			got, err := rater.Rate(context.Background(), b1OperatorInput(t, snapshot, tc.observations))
			if err != nil {
				t.Fatalf("Rate: %v", err)
			}
			if got.Completeness != economics.CompletenessComplete || smInspect(got).total != tc.wantTotal {
				t.Fatalf("completeness=%q total=%s, want complete %s", got.Completeness, smInspect(got).total, tc.wantTotal)
			}
			if len(got.SupportAdvisoryContexts) != 1 || got.SupportAdvisoryContexts[0].TariffContent != snapshot.Content {
				t.Fatalf("source contexts=%+v, want one context bound to the frozen v1 tariff", got.SupportAdvisoryContexts)
			}
			if got.SupportAdvisory != nil {
				t.Fatalf("clean assessment report=%+v, want nil", got.SupportAdvisory)
			}
		})
	}
}

func TestUncertaintyV1FixedOnlyKeepsSourceContextWithoutComponentAdvice(t *testing.T) {
	t.Parallel()
	key := r7Key("vendor:uncertainty_fixed_only_zero")
	rules := []economics.RatingRule{b1FixedRule(t, "uncertainty-fixed-only", economics.FixedFeeScopeCall, "5")}
	snapshot := uncertaintyV1Snapshot(t, f356Schema(t, "uncertainty-v1-fixed-only", rules, nil))
	rater, err := billing.NewReferenceRater(snapshot)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	observation := b1Observation(t, "uncertainty-v1-fixed-only", "b-leg-fixed-only", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator, b1Measure(t, key, "0"))
	got, err := rater.Rate(context.Background(), b1OperatorInput(t, snapshot, []metering.Observation{observation}))
	if err != nil {
		t.Fatalf("Rate fixed-only: %v", err)
	}
	if got.Completeness != economics.CompletenessComplete || len(got.Lines) != 1 || got.Lines[0].FixedFee == nil {
		t.Fatalf("fixed-only valuation=%+v, want one complete fixed line", got)
	}
	if len(got.SupportAdvisoryContexts) != 1 || got.SupportAdvisory != nil {
		t.Fatalf("contexts=%+v report=%+v, want one v1 context without component advice", got.SupportAdvisoryContexts, got.SupportAdvisory)
	}
}

func TestUncertaintyV1ProviderReportedKeepsNoLocalAdviceOrContext(t *testing.T) {
	t.Parallel()
	key := accKey("uncertainty_provider_reported")
	snapshot := uncertaintyV1Snapshot(t, f356Schema(t, "uncertainty-v1-provider-reported", []economics.RatingRule{accRule(t, "provider-reported-rule", key, "1")}, nil))
	rater, err := billing.NewReferenceRater(snapshot)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	observation := b1Observation(t, "uncertainty-v1-provider-reported", "b-leg-provider-reported", metering.OriginProvider, metering.BoundaryBackendIngress, metering.PerspectiveOperator, b1Measure(t, key, "1"))
	amount := b1Decimal(t, "7")
	observation.Charges = []metering.ReportedCharge{{
		ChargeItemID: "provider-reported-charge", Component: &key, Amount: &amount,
		Currency: "USD", Kind: metering.ChargeKindComponent,
	}}
	input := b1OperatorInput(t, snapshot, []metering.Observation{observation})
	input.Basis = economics.BasisProviderReported
	got, err := rater.Rate(context.Background(), input)
	if err != nil {
		t.Fatalf("Rate provider-reported: %v", err)
	}
	if got.Completeness != economics.CompletenessComplete || smInspect(got).total != "7/0" {
		t.Fatalf("provider-reported completeness=%q total=%s, want complete 7/0", got.Completeness, smInspect(got).total)
	}
	if len(got.SupportAdvisoryContexts) != 0 || got.SupportAdvisory != nil || got.Tariff.ID != "" {
		t.Fatalf("provider-reported valuation carried local context/advice: contexts=%+v report=%+v tariff=%+v", got.SupportAdvisoryContexts, got.SupportAdvisory, got.Tariff)
	}
}

func TestUncertaintyV1FixedAdditiveChargeStaysOutsideComponentAdvice(t *testing.T) {
	t.Parallel()
	fixture := uncertaintyAcceptance29(t)
	rules := append([]economics.RatingRule(nil), fixture.rules...)
	rules = append(rules, b1FixedRule(t, "uncertainty-additive-call-fee", economics.FixedFeeScopeCall, "7"))
	snapshot := uncertaintyV1Snapshot(t, f356Schema(t, "uncertainty-v1-additive-fixed", rules, fixture.relationships))
	rater, err := billing.NewReferenceRater(snapshot)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	got, err := rater.Rate(context.Background(), b1OperatorInput(t, snapshot, fixture.observations))
	if err != nil || got.Completeness != economics.CompletenessComplete || smInspect(got).total != "57/0" {
		t.Fatalf("fixed-additive result completeness=%q total=%s err=%v, want complete 57/0", got.Completeness, smInspect(got).total, err)
	}
	if len(got.Lines) != 4 {
		t.Fatalf("line count=%d, want two component siblings, aggregate observation line and one additive fixed fee", len(got.Lines))
	}
	fixedFees := 0
	for _, line := range got.Lines {
		if line.FixedFee != nil {
			fixedFees++
			if line.Amount == nil || line.Amount.Coefficient != "7" {
				t.Fatalf("additive fixed line=%+v, want one exact 7 charge", line)
			}
		}
	}
	if fixedFees != 1 {
		t.Fatalf("fixed fee line count=%d, want exactly one independently additive fee", fixedFees)
	}
	if got.SupportAdvisory == nil || len(got.SupportAdvisory.Pairs) != 1 {
		t.Fatalf("report=%+v, want only the unknown component sibling pair", got.SupportAdvisory)
	}
	pair := got.SupportAdvisory.Pairs[0]
	if pair.Left.CanonicalKey() != accKey("q_leaf_c").CanonicalKey() || pair.Right.CanonicalKey() != accKey("q_leaf_d").CanonicalKey() {
		t.Fatalf("reported pair=%+v, want component siblings only", pair)
	}
}

func TestUncertaintyV1ExactPositiveRetainedLinesIncludeSubnanoAndZeroQuantityMinimum(t *testing.T) {
	t.Parallel()
	parent := accKey("uncertainty_exact_parent")
	minimum := accKey("uncertainty_zero_quantity_minimum")
	subnano := accKey("uncertainty_subnano")
	minimumRule := accRule(t, "uncertainty-minimum-rule", minimum, "1")
	minimumRule.Kind = economics.RatingRuleMinimum
	minimumAmount := b1Decimal(t, "5")
	minimumRule.MinimumAmount = &minimumAmount
	rules := []economics.RatingRule{
		minimumRule,
		accRule(t, "uncertainty-subnano-rule", subnano, "0.0000000001"),
	}
	relationships := accEdge(nil, metering.RelationshipSubset, parent, minimum)
	relationships = accEdge(relationships, metering.RelationshipSubset, parent, subnano)
	legacySnapshot := f356Schema(t, "uncertainty-subnano-minimum", rules, relationships)
	legacyRater, err := billing.NewReferenceRater(legacySnapshot)
	if err != nil {
		t.Fatalf("NewReferenceRater legacy: %v", err)
	}
	observation := accObs(t, "uncertainty-subnano-minimum", "b-leg-subnano-minimum",
		b1Measure(t, minimum, "0"), b1Measure(t, subnano, "1"))
	legacy, err := legacyRater.Rate(context.Background(), b1OperatorInput(t, legacySnapshot, []metering.Observation{observation}))
	if err != nil {
		t.Fatalf("legacy Rate: %v", err)
	}
	enabledSnapshot := uncertaintyV1Snapshot(t, legacySnapshot)
	enabledRater, err := billing.NewReferenceRater(enabledSnapshot)
	if err != nil {
		t.Fatalf("NewReferenceRater v1: %v", err)
	}
	got, err := enabledRater.Rate(context.Background(), b1OperatorInput(t, enabledSnapshot, []metering.Observation{observation}))
	if err != nil {
		t.Fatalf("v1 Rate: %v", err)
	}
	if got.Completeness != economics.CompletenessComplete || !reflect.DeepEqual(got.Lines, legacy.Lines) || !reflect.DeepEqual(got.Totals, legacy.Totals) {
		t.Fatalf("v1 changed exact/minimum monetary outcome: v1 completeness=%q lines=%+v totals=%+v; legacy lines=%+v totals=%+v", got.Completeness, got.Lines, got.Totals, legacy.Lines, legacy.Totals)
	}
	if len(got.Totals) != 1 || got.Totals[0].Amount == nil || got.Totals[0].Amount.Coefficient != "50000000001" || got.Totals[0].Amount.Scale != 10 || got.Totals[0].RoundedAmount.NanoUnits != 5_000_000_000 {
		t.Fatalf("hand-expected exact/rounded total=%+v, want exact 5.0000000001 and rounded $5.00", got.Totals)
	}
	var sawMinimum, sawSubnano bool
	for _, line := range got.Lines {
		if line.Component == nil || line.Amount == nil || line.RoundedAmount == nil {
			continue
		}
		switch line.Component.CanonicalKey() {
		case minimum.CanonicalKey():
			sawMinimum = line.Quantity != nil && line.Quantity.Coefficient == "0" && line.Amount.Coefficient == "5"
		case subnano.CanonicalKey():
			sawSubnano = line.Quantity != nil && line.Quantity.Coefficient == "1" && line.Amount.Coefficient == "1" && line.Amount.Scale == 10 && line.RoundedAmount.NanoUnits == 0
		}
	}
	if !sawMinimum || !sawSubnano {
		t.Fatalf("retained lines do not prove zero-quantity minimum and rounded-to-zero subnano: %+v", got.Lines)
	}
	if got.SupportAdvisory == nil || len(got.SupportAdvisory.Pairs) != 1 {
		t.Fatalf("v1 report=%+v, want zero-quantity minimum and positive subnano exact charge as contributors", got.SupportAdvisory)
	}
	pair := got.SupportAdvisory.Pairs[0]
	if pair.Left.CanonicalKey() != minimum.CanonicalKey() && pair.Right.CanonicalKey() != minimum.CanonicalKey() ||
		pair.Left.CanonicalKey() != subnano.CanonicalKey() && pair.Right.CanonicalKey() != subnano.CanonicalKey() {
		t.Fatalf("v1 pair=%+v, want minimum and subnano contributors", pair)
	}
}

func TestUncertaintyV1ExcludesFreeZeroAndUnpricedComponentsFromAdvice(t *testing.T) {
	t.Parallel()
	parent := accKey("uncertainty_exclusion_parent")
	free := accKey("uncertainty_explicit_free")
	zero := accKey("uncertainty_zero_amount")
	unpriced := accKey("uncertainty_unpriced")
	paid := accKey("uncertainty_only_paid")
	rules := []economics.RatingRule{
		accRule(t, "uncertainty-free-rule", free, "0"),
		accRule(t, "uncertainty-zero-rule", zero, "1"),
		accRule(t, "uncertainty-paid-rule", paid, "1"),
	}
	relationships := []metering.ComponentRelationship{}
	for _, key := range []metering.ComponentKey{free, zero, unpriced, paid} {
		relationships = accEdge(relationships, metering.RelationshipSubset, parent, key)
	}
	snapshot := uncertaintyV1Snapshot(t, f356Schema(t, "uncertainty-v1-exclusions", rules, relationships))
	rater, err := billing.NewReferenceRater(snapshot)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	observation := accObs(t, "uncertainty-v1-exclusions", "b-leg-exclusions",
		b1Measure(t, free, "10"), b1Measure(t, zero, "0"), b1Measure(t, unpriced, "10"), b1Measure(t, paid, "10"))
	got, err := rater.Rate(context.Background(), b1OperatorInput(t, snapshot, []metering.Observation{observation}))
	if err == nil || got.Completeness != economics.CompletenessPartial {
		t.Fatalf("unpriced control error=%v completeness=%q, want typed partial result", err, got.Completeness)
	}
	if got.SupportAdvisory != nil {
		t.Fatalf("report=%+v, want nil with only one positive exact retained component", got.SupportAdvisory)
	}
	if got.Totals == nil || smInspect(got).total != "10/0" {
		t.Fatalf("money=%s lines=%+v, want only independently priced line at 10/0", smInspect(got).total, got.Lines)
	}
}

func TestUncertaintyV1SeparatesScopesAndSuppressesOverlappingLinesBeforeAdvice(t *testing.T) {
	t.Parallel()
	t.Run("cross scope siblings", func(t *testing.T) {
		t.Parallel()
		parent := accKey("uncertainty_cross_scope_parent")
		left, right := accKey("uncertainty_cross_scope_left"), accKey("uncertainty_cross_scope_right")
		relationships := accEdge(nil, metering.RelationshipSubset, parent, left)
		relationships = accEdge(relationships, metering.RelationshipSubset, parent, right)
		snapshot := uncertaintyV1Snapshot(t, f356Schema(t, "uncertainty-v1-cross-scope", []economics.RatingRule{accRule(t, "left", left, "1"), accRule(t, "right", right, "1")}, relationships))
		rater, err := billing.NewReferenceRater(snapshot)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		observations := []metering.Observation{
			b1Observation(t, "uncertainty-v1-cross-scope-left", "b-leg-cross-scope-left", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator, b1Measure(t, left, "20")),
			b1Observation(t, "uncertainty-v1-cross-scope-right", "b-leg-cross-scope-right", metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator, b1Measure(t, right, "30")),
		}
		got, err := rater.Rate(context.Background(), b1OperatorInput(t, snapshot, observations))
		if err != nil || got.Completeness != economics.CompletenessComplete || smInspect(got).total != "50/0" {
			t.Fatalf("cross-scope result completeness=%q total=%s err=%v, want complete 50/0", got.Completeness, smInspect(got).total, err)
		}
		if got.SupportAdvisory != nil {
			t.Fatalf("cross-scope report=%+v, want no pair across independent scopes", got.SupportAdvisory)
		}
	})
	t.Run("suppressed overlap and independent unknown pair", func(t *testing.T) {
		t.Parallel()
		root := accKey("uncertainty_suppressed_root")
		paidParent, paidChild := accKey("uncertainty_suppressed_parent"), accKey("uncertainty_suppressed_child")
		unresolvedParent := accKey("uncertainty_unsplit_parent")
		left, right := accKey("uncertainty_unsplit_left"), accKey("uncertainty_unsplit_right")
		relationships := accEdge(nil, metering.RelationshipSubset, root, paidParent)
		relationships = accEdge(relationships, metering.RelationshipSubset, root, unresolvedParent)
		relationships = accEdge(relationships, metering.RelationshipSubset, paidParent, paidChild)
		relationships = accEdge(relationships, metering.RelationshipSubset, unresolvedParent, left)
		relationships = accEdge(relationships, metering.RelationshipSubset, unresolvedParent, right)
		rules := []economics.RatingRule{
			accRule(t, "suppressed-parent", paidParent, "1"),
			accRule(t, "suppressed-child", paidChild, "1"),
			accRule(t, "unknown-left", left, "1"),
			accRule(t, "unknown-right", right, "1"),
		}
		snapshot := uncertaintyV1Snapshot(t, f356Schema(t, "uncertainty-v1-suppressed", rules, relationships))
		rater, err := billing.NewReferenceRater(snapshot)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		observation := accObs(t, "uncertainty-v1-suppressed", "b-leg-suppressed",
			b1Measure(t, paidParent, "10"), b1Measure(t, paidChild, "5"), b1Measure(t, left, "1"), b1Measure(t, right, "2"))
		got, err := rater.Rate(context.Background(), b1OperatorInput(t, snapshot, []metering.Observation{observation}))
		if !errors.Is(err, billing.ErrSchemaOverlapConflict) || got.Completeness != economics.CompletenessConflict {
			t.Fatalf("overlap result error=%v completeness=%q, want typed conflict", err, got.Completeness)
		}
		if len(got.Lines) != 2 || smInspect(got).total != "3/0" {
			t.Fatalf("retained lines=%+v total=%s, want only unaffected siblings at 3/0", got.Lines, smInspect(got).total)
		}
		if got.SupportAdvisory == nil || len(got.SupportAdvisory.Pairs) != 1 {
			t.Fatalf("report=%+v, want one independent unknown pair after conflict suppression", got.SupportAdvisory)
		}
		pair := got.SupportAdvisory.Pairs[0]
		if pair.Left.CanonicalKey() != left.CanonicalKey() || pair.Right.CanonicalKey() != right.CanonicalKey() {
			t.Fatalf("reported pair=%+v, want only the retained independent siblings", pair)
		}
	})
}

func TestUncertaintyV1PartialAndResolvedBranchResultsKeepContextWithoutFalsePair(t *testing.T) {
	t.Parallel()
	t.Run("partial local rating keeps context", func(t *testing.T) {
		t.Parallel()
		priced, unpriced := accKey("uncertainty_partial_priced"), accKey("uncertainty_partial_unpriced")
		rules := []economics.RatingRule{accRule(t, "uncertainty-partial-priced-rule", priced, "1")}
		snapshot := uncertaintyV1Snapshot(t, f356Schema(t, "uncertainty-v1-partial-context", rules, nil))
		rater, err := billing.NewReferenceRater(snapshot)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		observation := accObs(t, "uncertainty-v1-partial-context", "b-leg-partial-context",
			b1Measure(t, priced, "5"), b1Measure(t, unpriced, "2"))
		got, err := rater.Rate(context.Background(), b1OperatorInput(t, snapshot, []metering.Observation{observation}))
		if err == nil || got.Completeness != economics.CompletenessPartial {
			t.Fatalf("partial result error=%v completeness=%q, want partial retained valuation", err, got.Completeness)
		}
		if len(got.SupportAdvisoryContexts) != 1 || got.SupportAdvisoryContexts[0].TariffContent != snapshot.Content {
			t.Fatalf("partial result contexts=%+v, want frozen v1 source context", got.SupportAdvisoryContexts)
		}
		if smInspect(got).total != "5/0" {
			t.Fatalf("partial result money=%s, want only the known payable line at 5/0", smInspect(got).total)
		}
		if got.SupportAdvisory != nil {
			t.Fatalf("partial result report=%+v, want no pair from a single payable contributor", got.SupportAdvisory)
		}
	})
	t.Run("complete partition removes legacy unknown diagnostic", func(t *testing.T) {
		t.Parallel()
		root := r7Key("vendor:uncertainty_branch_root")
		branchB, branchC := r7Key("vendor:uncertainty_branch_b"), r7Key("vendor:uncertainty_branch_c")
		contradictionY := r7Key("vendor:uncertainty_contradiction_y")
		contradictionX := r7Key("vendor:uncertainty_contradiction_x")
		relationships := []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: root, Child: branchB},
			{Kind: metering.RelationshipPartition, Parent: root, Child: branchC},
			{Kind: metering.RelationshipSubset, Parent: contradictionY, Child: contradictionX},
		}
		rules := []economics.RatingRule{
			b1Rule(t, "v1-branch-b-rate", branchB, "1"),
			b1Rule(t, "v1-branch-c-rate", branchC, "1"),
			b1Rule(t, "v1-contradiction-y-rate", contradictionY, "1"),
			b1Rule(t, "v1-contradiction-x-rate", contradictionX, "1"),
		}
		snapshot := uncertaintyV1Snapshot(t, f356Schema(t, "uncertainty-v1-branch-root", rules, relationships))
		rater, err := billing.NewReferenceRater(snapshot)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		observation := b1Observation(t, "uncertainty-v1-branch-root", "b-leg-uncertainty-v1-branch",
			metering.OriginLocal, metering.BoundaryBackendIngress, metering.PerspectiveOperator,
			b1Measure(t, root, "100"), b1Measure(t, branchB, "40"), b1Measure(t, branchC, "60"),
			b1Measure(t, contradictionY, "10"), b1Measure(t, contradictionX, "20"))
		got, err := rater.Rate(context.Background(), b1OperatorInput(t, snapshot, []metering.Observation{observation}))
		if !errors.Is(err, billing.ErrSchemaOverlapConflict) || got.Completeness != economics.CompletenessConflict {
			t.Fatalf("branch result error=%v completeness=%q, want independent typed overlap conflict", err, got.Completeness)
		}
		if strings.Contains(err.Error(), "unknown containment intersection") {
			t.Fatalf("v1 branch-root error retained legacy unknown prose: %v", err)
		}
		if len(got.SupportAdvisoryContexts) != 1 || got.SupportAdvisory != nil {
			t.Fatalf("branch result contexts=%+v report=%+v, want source context and no pair for resolved complete branches", got.SupportAdvisoryContexts, got.SupportAdvisory)
		}
		if smInspect(got).total != "100/0" {
			t.Fatalf("branch result money=%s, want retained disjoint branch lines at 100/0", smInspect(got).total)
		}
	})
}

func uncertaintyAcceptance29(t *testing.T) accCase {
	t.Helper()
	for _, vector := range accVectors() {
		if vector.number == 29 {
			return vector.build(t)
		}
	}
	t.Fatal("acceptance vector 29 is missing")
	return accCase{}
}
