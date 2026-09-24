package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Repair4_DurableValuationIdentityKeepsEconomicContextsDistinct(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "phase9.v1"}
	price, err := metering.ParseDecimal("1")
	if err != nil {
		t.Fatal(err)
	}
	tariff, err := economics.BuildTariffSnapshot(economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "phase9-repair4-tariff", Version: "v1"}, RaterID: "reference"}, "USD", []economics.RatingRule{{ID: "image", Component: &key, Currency: "USD", UnitPrice: &price}})
	if err != nil {
		t.Fatal(err)
	}
	rater, err := billing.NewReferenceRater(tariff)
	if err != nil {
		t.Fatal(err)
	}
	observation := phase9RoundTripObservation(t, "identity-context", metering.OriginLocal, key, "2")
	base := economics.RatingInput{
		Version: 2, Perspective: metering.PerspectiveCustomer, Basis: economics.BasisLocalExpected,
		Subject: observation.Subject, Scope: "call:identity", Observations: []metering.Observation{observation},
		Rater: rater.Snapshot().Ref, RaterContent: &economics.SnapshotContentRef{ContentRef: "phase9://rater/v1", ContentHash: "1111111111111111111111111111111111111111111111111111111111111111"},
		Tariff: tariff.Ref, TariffContent: &tariff.Content, InputSetHash: "",
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "phase9://qualifiers/v1", ContentHash: "3333333333333333333333333333333333333333333333333333333333333333"},
	}
	variants := make([]economics.Valuation, 0, 6)
	for _, input := range []economics.RatingInput{
		base,
		func() economics.RatingInput { in := base.Clone(); in.Scope = "period:identity"; return in }(),
		func() economics.RatingInput {
			in := base.Clone()
			in.Perspective = metering.PerspectiveOperator
			return in
		}(),
		func() economics.RatingInput { in := base.Clone(); in.Subject.BLegID = "other-b-leg"; return in }(),
		func() economics.RatingInput {
			in := base.Clone()
			in.Payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer-1"}
			return in
		}(),
		func() economics.RatingInput {
			in := base.Clone()
			in.QualifierSnapshotRef.ContentHash = "4444444444444444444444444444444444444444444444444444444444444444"
			return in
		}(),
	} {
		valuation, rateErr := rater.Rate(ctx, input)
		if rateErr != nil {
			t.Fatalf("rate variant: %v", rateErr)
		}
		variants = append(variants, valuation)
	}
	for i, valuation := range variants {
		if err := store.AppendValuation(ctx, valuation); err != nil {
			t.Fatalf("append variant %d (%s): %v", i, valuation.ID, err)
		}
	}
	if err := store.AppendValuation(ctx, variants[0]); err != nil {
		t.Fatalf("same-context replay: %v", err)
	}
}

func TestPhase9Repair4_ValuationIdentityMigrationPreservesHistoricalRows(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if _, err := store.db.NewRaw(`DROP INDEX ` + billingValuationInputIndex).Exec(ctx); err != nil {
		t.Fatalf("drop current valuation index: %v", err)
	}
	// The Repair5 forward migration guard now includes the added context-hash
	// column. SQLite requires dependent triggers to be removed before a legacy
	// schema test can reconstruct the pre-column table.
	if _, err := store.db.NewRaw(`DROP TRIGGER IF EXISTS billing_v2_valuations_immutable_update`).Exec(ctx); err != nil {
		t.Fatalf("drop current valuation immutability trigger: %v", err)
	}
	if _, err := store.db.NewRaw(`ALTER TABLE billing_valuations DROP COLUMN valuation_context_hash`).Exec(ctx); err != nil {
		t.Fatalf("reconstruct pre-repair schema: %v", err)
	}
	if _, err := store.db.NewRaw(`DELETE FROM bun_billing_migrations WHERE name = ?`, BillingV2ValuationIdentityMigrationName).Exec(ctx); err != nil {
		t.Fatalf("reset valuation identity migration marker: %v", err)
	}
	if _, err := store.db.NewRaw(`INSERT INTO billing_valuations(store_id, valuation_id, valuation_version, perspective, basis, subject_kind, subject_id, tenant_id, scope, input_set_hash, rater_id, rater_version, tariff_id, tariff_version, policy_id, policy_version, qualifier_snapshot, canonical_json, fingerprint, projection_version, created_at_unix) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "test", "historical-valuation", 2, "customer", "provider_reported", "b_leg", "b-history", "", "call:history", "", "", "", "", "", "", "", "", "{}", "", 1, 1).Exec(ctx); err != nil {
		t.Fatalf("insert historical row: %v", err)
	}
	if err := billingV2ValuationIdentitySchemaUp(ctx, store.db); err != nil {
		t.Fatalf("valuation identity upgrade: %v", err)
	}
	if err := billingV2ValuationIdentitySchemaUp(ctx, store.db); err != nil {
		t.Fatalf("valuation identity idempotent upgrade: %v", err)
	}
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE valuation_id = ?`, "historical-valuation").Scan(ctx, &count); err != nil {
		t.Fatalf("historical row lookup: %v", err)
	}
	if count != 1 {
		t.Fatalf("historical row count=%d, want 1", count)
	}
	var contextHash string
	if err := store.db.NewRaw(`SELECT valuation_context_hash FROM billing_valuations WHERE valuation_id = ?`, "historical-valuation").Scan(ctx, &contextHash); err != nil {
		t.Fatalf("context hash lookup: %v", err)
	}
	if contextHash != "" {
		t.Fatalf("historical context hash=%q, want empty unknown value", contextHash)
	}
	var missing string
	if err := store.db.NewRaw(`SELECT name FROM pragma_table_info('billing_valuations') WHERE name = 'valuation_context_hash'`).Scan(ctx, &missing); err != nil {
		t.Fatalf("context column lookup: %v", err)
	}
	if missing == "" {
		t.Fatalf("context column missing after migration")
	}
}
