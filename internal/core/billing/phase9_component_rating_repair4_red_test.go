package billing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Repair4_UnavailableBLegDoesNotPoisonCompleteSibling(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	unavailable := phase9Observation(t, "unavailable-b-leg", metering.OriginLocal, key, "0")
	unavailable.Authority = metering.AuthorityUnavailableClaim
	unavailable.Measures[0].Value = nil
	unavailable.Measures[0].Quality = metering.QualityUnavailable
	complete := phase9Observation(t, "complete-b-leg", metering.OriginLocal, key, "2")
	complete.Subject.BLegID = "b-complete"
	complete.Correlation.BLegID = "b-complete"
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{unavailable, complete}, tariff)
	got, rateErr := rater.Rate(context.Background(), input)
	if !errors.Is(rateErr, ErrQuantityIncomplete) && !errors.Is(rateErr, ErrRatingEvidenceMissing) {
		t.Fatalf("rating error=%v, want local incomplete evidence", rateErr)
	}
	var completeLine, incompleteLine *economics.LineItem
	for i := range got.Lines {
		line := &got.Lines[i]
		if line.Status == economics.RatingLineRated {
			completeLine = line
		}
		if line.Status == economics.RatingLineQuantityIncomplete {
			incompleteLine = line
		}
	}
	if completeLine == nil || completeLine.Amount == nil || completeLine.Amount.CanonicalString() != "2/0" {
		t.Fatalf("complete sibling line=%+v, want independently rated amount 2", completeLine)
	}
	if incompleteLine == nil {
		t.Fatalf("valuation lines=%+v, want an affected incomplete partition", got.Lines)
	}
	if len(got.MissingObservations) == 0 {
		t.Fatalf("valuation=%+v, want audit diagnostic for unavailable partition", got)
	}
}

func TestPhase9Repair4_EffectiveReplayIgnoresSupersededUnknownLinks(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	for _, tc := range []struct {
		name   string
		origin string
		basis  economics.ValuationBasis
	}{
		{name: "expected", origin: metering.OriginLocal, basis: economics.BasisLocalExpected},
		{name: "provider quantity", origin: metering.OriginProvider, basis: economics.BasisProviderQuantityLocal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stale := phase9Observation(t, "stale-unknown-"+tc.name, tc.origin, key, "99")
			stale.Semantics = metering.SemanticsCorrection
			stale.Supersedes = []metering.ObservationRef{{StoreID: "store", ObservationID: "not-yet-known", Revision: 1, PayloadHash: strings.Repeat("a", 64)}}
			stale.Sequence = 1
			staleRef, refErr := stale.Ref(stale.Subject.StoreID)
			if refErr != nil {
				t.Fatalf("stale ref: %v", refErr)
			}
			replacement := phase9Observation(t, "replacement-"+tc.name, tc.origin, key, "2")
			replacement.Semantics = metering.SemanticsReplacement
			replacement.Supersedes = []metering.ObservationRef{staleRef}
			replacement.Sequence = 2
			input := phase9RatingInput(t, tc.basis, []metering.Observation{replacement, stale}, tariff)
			got, rateErr := rater.Rate(context.Background(), input)
			if rateErr != nil {
				t.Fatalf("effective replay rating: %v", rateErr)
			}
			if got.Completeness != economics.CompletenessComplete || len(got.Totals) != 1 || got.Totals[0].Amount == nil || got.Totals[0].Amount.CanonicalString() != "2/0" {
				t.Fatalf("valuation=%+v, want complete replacement amount 2", got)
			}
			if len(got.InputObservations) != 2 {
				t.Fatalf("audit refs=%+v, want stale and replacement refs", got.InputObservations)
			}
		})
	}
}

func TestPhase9Repair4_ProviderReportedEffectiveReplayIgnoresSupersededUnknownLinks(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	stale := phase9Observation(t, "provider-stale-unknown", metering.OriginProvider, key, "1")
	stale.Semantics = metering.SemanticsCorrection
	stale.Supersedes = []metering.ObservationRef{{StoreID: "store", ObservationID: "provider-not-yet-known", Revision: 1, PayloadHash: strings.Repeat("b", 64)}}
	stale.Charges = []metering.ReportedCharge{{ChargeItemID: "provider-charge", Component: &key, Amount: phase9Decimal("99"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	staleRef, err := stale.Ref(stale.Subject.StoreID)
	if err != nil {
		t.Fatalf("stale ref: %v", err)
	}
	replacement := phase9Observation(t, "provider-replacement", metering.OriginProvider, key, "1")
	replacement.Semantics = metering.SemanticsReplacement
	replacement.Supersedes = []metering.ObservationRef{staleRef}
	replacement.Sequence = 2
	replacement.Charges = []metering.ReportedCharge{{ChargeItemID: "provider-charge", Component: &key, Amount: phase9Decimal("7"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{replacement, stale})
	input.Tariff = economics.RatingSnapshotRef{}
	input.TariffContent = nil
	input.RaterContent = nil
	got, rateErr := RateProviderReported(context.Background(), input)
	if rateErr != nil {
		t.Fatalf("provider effective replay rating: %v", rateErr)
	}
	if got.Completeness != economics.CompletenessComplete || len(got.Totals) != 1 || got.Totals[0].Amount == nil || got.Totals[0].Amount.CanonicalString() != "7/0" {
		t.Fatalf("valuation=%+v, want complete replacement charge amount 7", got)
	}
	if len(got.InputObservations) != 2 {
		t.Fatalf("audit refs=%+v, want stale and replacement refs", got.InputObservations)
	}
}

func TestPhase9Repair4_ProviderUnavailableBLegDoesNotPoisonCompleteSibling(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	unavailable := phase9Observation(t, "provider-unavailable-b-leg", metering.OriginProvider, key, "0")
	unavailable.Authority = metering.AuthorityUnavailableClaim
	unavailable.Measures = nil
	complete := phase9Observation(t, "provider-complete-b-leg", metering.OriginProvider, key, "1")
	complete.Subject.BLegID = "b-provider-complete"
	complete.Correlation.BLegID = "b-provider-complete"
	complete.Charges = []metering.ReportedCharge{{ChargeItemID: "complete-charge", Component: &key, Amount: phase9Decimal("7"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{unavailable, complete})
	input.Tariff = economics.RatingSnapshotRef{}
	input.TariffContent = nil
	input.RaterContent = nil
	got, rateErr := RateProviderReported(context.Background(), input)
	if !errors.Is(rateErr, ErrRatingEvidenceMissing) {
		t.Fatalf("provider rating error=%v, want unavailable evidence", rateErr)
	}
	if len(got.Lines) != 1 || got.Lines[0].Status != economics.RatingLineProviderReported || got.Totals[0].Amount == nil || got.Totals[0].Amount.CanonicalString() != "7/0" {
		t.Fatalf("provider valuation=%+v, want complete sibling charge retained", got)
	}
}

func TestPhase9Repair4_PendingEvidenceIsLocalToItsBLeg(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	pending := phase9Observation(t, "pending-b-leg", metering.OriginLocal, key, "3")
	pending.Semantics = metering.SemanticsReplacement
	pending.Supersedes = []metering.ObservationRef{{StoreID: "store", ObservationID: "missing-predecessor", Revision: 1, PayloadHash: strings.Repeat("c", 64)}}
	complete := phase9Observation(t, "complete-pending-sibling", metering.OriginLocal, key, "2")
	complete.Subject.BLegID = "b-pending-sibling"
	complete.Correlation.BLegID = "b-pending-sibling"
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{pending, complete}, tariff)
	got, rateErr := rater.Rate(context.Background(), input)
	if !errors.Is(rateErr, ErrRatingEvidenceMissing) {
		t.Fatalf("rating error=%v, want pending evidence", rateErr)
	}
	var completeLine *economics.LineItem
	for i := range got.Lines {
		if got.Lines[i].Status == economics.RatingLineRated {
			completeLine = &got.Lines[i]
		}
	}
	if completeLine == nil || completeLine.Amount == nil || completeLine.Amount.CanonicalString() != "2/0" {
		t.Fatalf("valuation=%+v, want complete sibling rated independently", got)
	}
}

func TestPhase9Repair4_ProviderPendingEvidenceIsLocalToItsBLeg(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	pending := phase9Observation(t, "provider-pending-b-leg", metering.OriginProvider, key, "1")
	pending.Semantics = metering.SemanticsReplacement
	pending.Supersedes = []metering.ObservationRef{{StoreID: "store", ObservationID: "provider-missing-predecessor", Revision: 1, PayloadHash: strings.Repeat("d", 64)}}
	pending.Charges = []metering.ReportedCharge{{ChargeItemID: "pending-charge", Component: &key, Amount: phase9Decimal("99"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	complete := phase9Observation(t, "provider-complete-pending-sibling", metering.OriginProvider, key, "1")
	complete.Subject.BLegID = "b-provider-pending-sibling"
	complete.Correlation.BLegID = "b-provider-pending-sibling"
	complete.Charges = []metering.ReportedCharge{{ChargeItemID: "complete-pending-charge", Component: &key, Amount: phase9Decimal("7"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{pending, complete})
	input.Tariff = economics.RatingSnapshotRef{}
	input.TariffContent = nil
	input.RaterContent = nil
	got, rateErr := RateProviderReported(context.Background(), input)
	if !errors.Is(rateErr, ErrRatingEvidenceMissing) {
		t.Fatalf("provider rating error=%v, want pending evidence", rateErr)
	}
	if len(got.Lines) != 1 || got.Lines[0].ItemID != "complete-pending-charge" || got.Lines[0].Status != economics.RatingLineProviderReported || got.Totals[0].Amount == nil || got.Totals[0].Amount.CanonicalString() != "7/0" {
		t.Fatalf("provider valuation=%+v, want complete sibling only", got)
	}
}

func TestPhase9Repair4_ExplicitConversionFailsClosedAtEvaluation(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{{ID: "conversion", Kind: economics.RatingRuleConversion, Component: &key, Currency: "USD", ConversionSchema: "image-to-token-v1"}})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{phase9Observation(t, "conversion", metering.OriginLocal, key, "1")}, tariff)
	if _, err := rater.Rate(context.Background(), input); !errors.Is(err, ErrRateUnsupported) {
		t.Fatalf("conversion evaluation error=%v, want %v", err, ErrRateUnsupported)
	}
}

func TestPhase9Repair4_ValuationIdentityIncludesEconomicContext(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	observation := phase9Observation(t, "identity-context", metering.OriginLocal, key, "2")
	base := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{observation}, tariff)
	variants := []economics.RatingInput{base}
	byScope := base.Clone()
	byScope.Scope = "period:2026-09"
	variants = append(variants, byScope)
	byPerspective := base.Clone()
	byPerspective.Perspective = metering.PerspectiveOperator
	variants = append(variants, byPerspective)
	bySubject := base.Clone()
	bySubject.Subject.BLegID = "other-b-leg"
	variants = append(variants, bySubject)
	byPayer := base.Clone()
	byPayer.Payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer-1"}
	variants = append(variants, byPayer)
	ids := make(map[string]struct{}, len(variants))
	for i, input := range variants {
		got, rateErr := rater.Rate(context.Background(), input)
		if rateErr != nil {
			t.Fatalf("variant %d rating: %v", i, rateErr)
		}
		if _, exists := ids[got.ID]; exists {
			t.Fatalf("variant %d reused valuation identity %q", i, got.ID)
		}
		ids[got.ID] = struct{}{}
	}
	replay, rateErr := rater.Rate(context.Background(), base)
	if rateErr != nil {
		t.Fatalf("same-context replay: %v", rateErr)
	}
	first, rateErr := rater.Rate(context.Background(), base)
	if rateErr != nil {
		t.Fatalf("same-context first: %v", rateErr)
	}
	if replay.ID != first.ID {
		t.Fatalf("same-context replay changed identity: %q/%q", first.ID, replay.ID)
	}
}
