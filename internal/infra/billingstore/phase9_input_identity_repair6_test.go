package billingstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Repair6_DurableValuationUsesVerifiedInputSetIdentity(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "repair6.v1"}
	price, err := metering.ParseDecimal("1")
	if err != nil {
		t.Fatal(err)
	}
	tariff, err := economics.BuildTariffSnapshot(economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "repair6-tariff", Version: "v1"}, RaterID: "reference"}, "USD", []economics.RatingRule{{ID: "image", Component: &key, Currency: "USD", UnitPrice: &price}})
	if err != nil {
		t.Fatal(err)
	}
	rater, err := billing.NewReferenceRater(tariff)
	if err != nil {
		t.Fatal(err)
	}
	inputFor := func(id string) economics.RatingInput {
		observation := phase9RoundTripObservation(t, id, metering.OriginLocal, key, "2")
		return economics.RatingInput{
			Version: 2, Perspective: metering.PerspectiveCustomer, Basis: economics.BasisLocalExpected,
			Subject: observation.Subject, Scope: "call:repair6", Observations: []metering.Observation{observation},
			Rater: rater.Snapshot().Ref, RaterContent: &economics.SnapshotContentRef{ContentRef: "repair6://rater/v1", ContentHash: strings.Repeat("1", 64)},
			Tariff: tariff.Ref, TariffContent: &tariff.Content,
			QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "repair6://qualifiers/v1", ContentHash: strings.Repeat("2", 64)},
		}
	}
	first, err := rater.Rate(ctx, inputFor("repair6-durable-first"))
	if err != nil {
		t.Fatalf("first rate: %v", err)
	}
	second, err := rater.Rate(ctx, inputFor("repair6-durable-second"))
	if err != nil {
		t.Fatalf("second rate: %v", err)
	}
	if first.InputSetHash == second.InputSetHash {
		t.Fatalf("distinct observation sets share input hash %q", first.InputSetHash)
	}
	if err := store.AppendValuation(ctx, first); err != nil {
		t.Fatalf("append first: %v", err)
	}
	if err := store.AppendValuation(ctx, first); err != nil {
		t.Fatalf("same valuation replay: %v", err)
	}
	if err := store.AppendValuation(ctx, second); err != nil {
		t.Fatalf("append second: %v", err)
	}
	emptyHash, err := rater.Rate(ctx, inputFor("repair6-empty-hash-observation"))
	if err != nil {
		t.Fatalf("empty-hash fixture rate: %v", err)
	}
	emptyHash.ID = "repair6-empty-hash"
	emptyHash.InputSetHash = ""
	if err := store.AppendValuation(ctx, emptyHash); err != nil {
		t.Fatalf("empty caller hash should be filled at durable boundary: %v", err)
	}

	tampered := second.Clone()
	tampered.ID = "repair6-tampered"
	tampered.InputSetHash = first.InputSetHash
	if err := store.AppendValuation(ctx, tampered); !errors.Is(err, economics.ErrInputSetHashMismatch) {
		t.Fatalf("durable mismatched hash error=%v, want ErrInputSetHashMismatch", err)
	}
}

func TestPhase9Repair6_DurableQualifierContextHashMatchesInMemoryIdentity(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "repair6.qualifier.v1"}
	priceOne, err := metering.ParseDecimal("1")
	if err != nil {
		t.Fatal(err)
	}
	priceTwo, err := metering.ParseDecimal("2")
	if err != nil {
		t.Fatal(err)
	}
	us := economics.RatingRule{ID: "image-us", Component: &key, Currency: "USD", UnitPrice: &priceOne, Conditions: []economics.QualifierCondition{{Name: "region", Value: "us"}}}
	eu := economics.RatingRule{ID: "image-eu", Component: &key, Currency: "USD", UnitPrice: &priceTwo, Conditions: []economics.QualifierCondition{{Name: "region", Value: "eu"}}}
	tariff, err := economics.BuildTariffSnapshot(economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "repair6-qualifier-tariff", Version: "v1"}, RaterID: "reference"}, "USD", []economics.RatingRule{us, eu})
	if err != nil {
		t.Fatal(err)
	}
	rater, err := billing.NewReferenceRater(tariff)
	if err != nil {
		t.Fatal(err)
	}
	observation := phase9RoundTripObservation(t, "repair6-qualifier-observation", metering.OriginLocal, key, "2")
	input := economics.RatingInput{
		Version: 2, Perspective: metering.PerspectiveCustomer, Basis: economics.BasisLocalExpected,
		Subject: observation.Subject, Scope: "call:repair6", Observations: []metering.Observation{observation},
		Rater: rater.Snapshot().Ref, RaterContent: &economics.SnapshotContentRef{ContentRef: "repair6://rater/v1", ContentHash: strings.Repeat("1", 64)},
		Tariff: tariff.Ref, TariffContent: &tariff.Content,
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "repair6://qualifiers/v1", ContentHash: strings.Repeat("2", 64)},
	}
	input.EffectiveQualifiers = []metering.Dimension{{Name: "region", Value: "us"}, {Name: "tier", Value: "standard"}}
	usValuation, err := rater.Rate(ctx, input)
	if err != nil {
		t.Fatalf("US rate: %v", err)
	}
	input.EffectiveQualifiers = []metering.Dimension{{Name: "tier", Value: "standard"}, {Name: "region", Value: "eu"}}
	euValuation, err := rater.Rate(ctx, input)
	if err != nil {
		t.Fatalf("EU rate: %v", err)
	}
	if usValuation.ID == euValuation.ID || usValuation.ContextHash() == euValuation.ContextHash() {
		t.Fatalf("qualifier values collapsed identity: US=%q/%q EU=%q/%q", usValuation.ID, usValuation.ContextHash(), euValuation.ID, euValuation.ContextHash())
	}
	if err := store.AppendValuation(ctx, usValuation); err != nil {
		t.Fatalf("append US: %v", err)
	}
	if err := store.AppendValuation(ctx, euValuation); err != nil {
		t.Fatalf("append EU: %v", err)
	}
	input.EffectiveQualifiers = []metering.Dimension{{Name: "region", Value: "us"}, {Name: "tier", Value: "standard"}}
	reordered, err := rater.Rate(ctx, input)
	if err != nil {
		t.Fatalf("reordered US rate: %v", err)
	}
	if reordered.ID != usValuation.ID || reordered.ContextHash() != usValuation.ContextHash() {
		t.Fatalf("qualifier order changed identity: got=%q/%q want=%q/%q", reordered.ID, reordered.ContextHash(), usValuation.ID, usValuation.ContextHash())
	}
	if err := store.AppendValuation(ctx, reordered); err != nil {
		t.Fatalf("reordered replay: %v", err)
	}
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, "test").Scan(ctx, &count); err != nil {
		t.Fatalf("valuation count: %v", err)
	}
	if count != 2 {
		t.Fatalf("durable qualifier valuation count=%d, want 2", count)
	}
}
