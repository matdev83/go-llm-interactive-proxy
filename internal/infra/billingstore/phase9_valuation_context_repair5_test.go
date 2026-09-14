package billingstore

import (
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Repair5_DurableContextVariantsCoexistAndReplayIdempotently(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "phase9.v1"}
	priceOne, err := metering.ParseDecimal("1")
	if err != nil {
		t.Fatal(err)
	}
	priceTwo, err := metering.ParseDecimal("2")
	if err != nil {
		t.Fatal(err)
	}
	baseRef := economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "repair5-tariff", Version: "v1"}, RaterID: "reference"}
	tariffOne, err := economics.BuildTariffSnapshot(baseRef, "USD", []economics.RatingRule{{ID: "image", Component: &key, Currency: "USD", UnitPrice: &priceOne}})
	if err != nil {
		t.Fatal(err)
	}
	tariffTwo, err := economics.BuildTariffSnapshot(baseRef, "USD", []economics.RatingRule{{ID: "image", Component: &key, Currency: "USD", UnitPrice: &priceTwo}})
	if err != nil {
		t.Fatal(err)
	}
	otherTariffRef := economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "repair5-other-tariff", Version: "v1"}, RaterID: "reference"}
	otherTariff, err := economics.BuildTariffSnapshot(otherTariffRef, "USD", []economics.RatingRule{{ID: "image", Component: &key, Currency: "USD", UnitPrice: &priceOne}})
	if err != nil {
		t.Fatal(err)
	}
	raterOne, err := billing.NewReferenceRater(tariffOne)
	if err != nil {
		t.Fatal(err)
	}
	raterTwo, err := billing.NewReferenceRater(tariffTwo)
	if err != nil {
		t.Fatal(err)
	}
	otherRater, err := billing.NewReferenceRater(otherTariff)
	if err != nil {
		t.Fatal(err)
	}
	observation := phase9RoundTripObservation(t, "repair5-context", metering.OriginLocal, key, "2")
	raterRef := economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "repair5-rater", Version: "v1"}, RaterID: "reference"}
	policyRef := economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "repair5-policy", Version: "v1"}, PolicyID: "customer"}
	base := economics.RatingInput{
		Version: 2, Perspective: metering.PerspectiveCustomer, Basis: economics.BasisLocalExpected,
		Subject: observation.Subject, Scope: "call:repair5", Observations: []metering.Observation{observation},
		Rater: raterRef, RaterContent: &economics.SnapshotContentRef{ContentRef: "phase9://rater/v1", ContentHash: strings.Repeat("1", 64)},
		Tariff: tariffOne.Ref, TariffContent: &tariffOne.Content, Policy: policyRef,
		PolicyContent:        &economics.SnapshotContentRef{ContentRef: "phase9://policy/v1", ContentHash: strings.Repeat("2", 64)},
		InputSetHash:         "",
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "phase9://qualifiers/v1", ContentHash: strings.Repeat("3", 64)},
	}
	variants := []struct {
		name  string
		rater *billing.ReferenceRater
		input economics.RatingInput
	}{
		{name: "base", rater: raterOne, input: base},
		{name: "rater identity", rater: raterOne, input: func() economics.RatingInput {
			in := base.Clone()
			in.Rater.ID = "repair5-other-rater"
			return in
		}()},
		{name: "rater content", rater: raterOne, input: func() economics.RatingInput {
			in := base.Clone()
			in.RaterContent.ContentHash = strings.Repeat("4", 64)
			return in
		}()},
		{name: "tariff content", rater: raterTwo, input: func() economics.RatingInput {
			in := base.Clone()
			in.TariffContent = &tariffTwo.Content
			return in
		}()},
		{name: "tariff identity", rater: otherRater, input: func() economics.RatingInput {
			in := base.Clone()
			in.Tariff = otherTariff.Ref
			in.TariffContent = &otherTariff.Content
			return in
		}()},
		{name: "policy identity", rater: raterOne, input: func() economics.RatingInput {
			in := base.Clone()
			in.Policy.ID = "repair5-other-policy"
			in.PolicyContent.ContentRef = "phase9://policy/other-v1"
			return in
		}()},
		{name: "policy content", rater: raterOne, input: func() economics.RatingInput {
			in := base.Clone()
			in.PolicyContent.ContentHash = strings.Repeat("5", 64)
			return in
		}()},
	}
	valuations := make([]economics.Valuation, 0, len(variants))
	seenIDs := make(map[string]struct{}, len(variants))
	for _, variant := range variants {
		valuation, rateErr := variant.rater.Rate(ctx, variant.input)
		if rateErr != nil {
			t.Fatalf("rate %s: %v", variant.name, rateErr)
		}
		if _, exists := seenIDs[valuation.ID]; exists {
			t.Fatalf("variant %s reused valuation identity %q", variant.name, valuation.ID)
		}
		seenIDs[valuation.ID] = struct{}{}
		valuations = append(valuations, valuation)
	}
	for i, valuation := range valuations {
		if err := store.AppendValuation(ctx, valuation); err != nil {
			t.Fatalf("append %s (%d): %v", variants[i].name, i, err)
		}
	}
	if err := store.AppendValuation(ctx, valuations[0]); err != nil {
		t.Fatalf("same-context replay: %v", err)
	}
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ? AND input_set_hash = ?`, "test", valuations[0].InputSetHash).Scan(ctx, &count); err != nil {
		t.Fatalf("variant count: %v", err)
	}
	if count != len(variants) {
		t.Fatalf("durable context variant count=%d, want %d", count, len(variants))
	}
	var storedHash string
	if err := store.db.NewRaw(`SELECT valuation_context_hash FROM billing_valuations WHERE store_id = ? AND valuation_id = ?`, "test", valuations[0].ID).Scan(ctx, &storedHash); err != nil {
		t.Fatalf("stored context hash: %v", err)
	}
	if storedHash != valuations[0].ContextHash() {
		t.Fatalf("stored context hash=%q, valuation context hash=%q", storedHash, valuations[0].ContextHash())
	}
}
