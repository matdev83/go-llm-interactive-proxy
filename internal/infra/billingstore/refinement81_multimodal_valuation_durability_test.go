package billingstore

// Task 8.1 remediation Findings 1+2: every multimodal direction vector is
// rated by the production ReferenceRater, persisted through the real SQLite
// billingstore valuation API, and read back through GetValuation after a
// file-backed close/reopen. The full economic line tuple (quantity,
// presence, native unit, canonical component + direction, schema ID and
// dimensions, rate ID/version, rate amount, line amount, currency, subject
// and observation/valuation lineage) is asserted from production outputs
// against explicit literal expectations. Test-only; production rating,
// migration and store code are reused unchanged.

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	_ "modernc.org/sqlite"
)

const v81StoreID = "ref81-billing"

type v81Vector struct {
	name             string
	component        string
	direction        metering.FlowDirection
	unit             string
	dimensions       []metering.Dimension
	quantity         string
	providerRate     string
	customerRate     string
	expectedSupplier string
	expectedCustomer string
	backendBoundary  metering.Boundary
}

func v81Vectors() []v81Vector {
	return []v81Vector{
		{
			name: "image-input", component: metering.ComponentImage, direction: metering.DirectionInput,
			unit:             metering.UnitImage,
			dimensions:       []metering.Dimension{{Name: "quality", Value: "hd"}, {Name: "transform", Value: "resize"}},
			quantity:         "2",
			providerRate:     "3",
			customerRate:     "5",
			expectedSupplier: "6",
			expectedCustomer: "10",
			backendBoundary:  metering.BoundaryBackendEgress,
		},
		{
			name: "image-output", component: metering.ComponentImage, direction: metering.DirectionOutput,
			unit:             metering.UnitImage,
			dimensions:       []metering.Dimension{{Name: "quality", Value: "hd"}, {Name: "transform", Value: "transcode"}},
			quantity:         "1",
			providerRate:     "4",
			customerRate:     "7",
			expectedSupplier: "4",
			expectedCustomer: "7",
			backendBoundary:  metering.BoundaryBackendIngress,
		},
		{
			name: "audio-input", component: metering.ComponentAudio, direction: metering.DirectionInput,
			unit:             metering.UnitSecond,
			dimensions:       []metering.Dimension{{Name: "codec", Value: "pcm"}, {Name: "transform", Value: "trim"}},
			quantity:         "12.5",
			providerRate:     "0.5",
			customerRate:     "2",
			expectedSupplier: "6.25",
			expectedCustomer: "25",
			backendBoundary:  metering.BoundaryBackendEgress,
		},
		{
			name: "audio-output", component: metering.ComponentAudio, direction: metering.DirectionOutput,
			unit:             metering.UnitSecond,
			dimensions:       []metering.Dimension{{Name: "codec", Value: "opus"}, {Name: "transform", Value: "resample"}},
			quantity:         "8.0",
			providerRate:     "1",
			customerRate:     "3",
			expectedSupplier: "8",
			expectedCustomer: "24",
			backendBoundary:  metering.BoundaryBackendIngress,
		},
		{
			name: "video-input", component: metering.ComponentVideo, direction: metering.DirectionInput,
			unit:             metering.UnitToken,
			dimensions:       []metering.Dimension{{Name: "tokenizer", Value: "provider_native"}},
			quantity:         "4096",
			providerRate:     "0.001",
			customerRate:     "0.002",
			expectedSupplier: "4.096",
			expectedCustomer: "8.192",
			backendBoundary:  metering.BoundaryBackendEgress,
		},
		{
			name: "video-output", component: metering.ComponentVideo, direction: metering.DirectionOutput,
			unit:             metering.UnitSecond,
			dimensions:       []metering.Dimension{{Name: "generation", Value: "provider_generated"}},
			quantity:         "8",
			providerRate:     "2",
			customerRate:     "5",
			expectedSupplier: "16",
			expectedCustomer: "40",
			backendBoundary:  metering.BoundaryBackendIngress,
		},
		{
			name: "document-input", component: metering.ComponentDocument, direction: metering.DirectionInput,
			unit:             metering.UnitPage,
			dimensions:       []metering.Dimension{{Name: "format", Value: "pdf"}, {Name: "transform", Value: "extract"}},
			quantity:         "4",
			providerRate:     "1.5",
			customerRate:     "2.5",
			expectedSupplier: "6",
			expectedCustomer: "10",
			backendBoundary:  metering.BoundaryBackendEgress,
		},
		{
			name: "document-output", component: metering.ComponentDocument, direction: metering.DirectionOutput,
			unit:             metering.UnitPage,
			dimensions:       []metering.Dimension{{Name: "format", Value: "pdf"}, {Name: "transform", Value: "paginate"}},
			quantity:         "2",
			providerRate:     "3",
			customerRate:     "6",
			expectedSupplier: "6",
			expectedCustomer: "12",
			backendBoundary:  metering.BoundaryBackendIngress,
		},
	}
}

func v81Key(v v81Vector) metering.ComponentKey {
	return metering.ComponentKey{
		Direction:  v.direction,
		Component:  v.component,
		Unit:       v.unit,
		SchemaID:   "refinement81.cert.v1",
		Dimensions: append([]metering.Dimension(nil), v.dimensions...),
	}
}

func v81Decimal(t *testing.T, raw string) *metering.Decimal {
	t.Helper()
	d, err := metering.ParseDecimal(raw)
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", raw, err)
	}
	return &d
}

func v81Canonical(raw string) string {
	d, err := metering.ParseDecimal(raw)
	if err != nil {
		panic(err)
	}
	return d.CanonicalString()
}

func v81Observation(t *testing.T, id, bLegID, origin string, boundary metering.Boundary, perspective metering.EconomicPerspective, key metering.ComponentKey, quantity string) metering.Observation {
	t.Helper()
	acquisition := metering.AcquisitionLocalTransport
	if origin == metering.OriginProvider {
		acquisition = metering.AcquisitionProviderResponse
	}
	now := time.Unix(1_700_000_081, 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event",
		Revision: 1, StreamID: id + "-stream", Sequence: 1,
		Origin: origin, Acquisition: acquisition, Authority: metering.AuthorityObservedClaim,
		Perspective: perspective, Boundary: boundary, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: v81StoreID,
			ALegID: "a-81", BillingCallID: "call-81", BLegID: bLegID,
		},
		Correlation: metering.CorrelationV2{
			StoreID: v81StoreID, ALegID: "a-81", BillingCallID: "call-81", BLegID: bLegID,
		},
		Semantics:  metering.SemanticsDelta,
		ObservedAt: now, ReceivedAt: now, MappingRef: "refinement81.cert.v1",
		Measures: []metering.Measure{{
			Key: key, Value: v81Decimal(t, quantity), Quality: metering.QualityObserved,
			MethodRef: "refinement81.cert.v1",
		}},
	}
}

func v81LinearRule(id string, key metering.ComponentKey, price string) economics.RatingRule {
	d, err := metering.ParseDecimal(price)
	if err != nil {
		panic(err)
	}
	return economics.RatingRule{ID: id, Component: &key, Currency: "USD", UnitPrice: &d}
}

func v81Tariff(t *testing.T, id string, rules []economics.RatingRule) economics.TariffSnapshot {
	t.Helper()
	return economics.NewTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: id, Version: "v1"}, RaterID: "reference"},
		"USD", rules,
	)
}

func v81SupplierInput(t *testing.T, tariff economics.TariffSnapshot, observations []metering.Observation) economics.PostUsageRatingInput {
	t.Helper()
	return economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderQuantityLocal,
		Subject: observations[0].Subject, Scope: "call:refinement81", Observations: observations,
		Rater:                economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "refinement81-rater", Version: "v1"}, RaterID: "reference"},
		RaterContent:         &economics.SnapshotContentRef{ContentRef: "catalog://refinement81/rater/v1", ContentHash: strings.Repeat("1", 64)},
		Tariff:               tariff.Ref,
		TariffContent:        &tariff.Content,
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "catalog://refinement81/qualifiers/v1", ContentHash: strings.Repeat("4", 64)},
		AsOf:                 time.Unix(1_700_000_082, 0).UTC(),
	}
}

func v81CustomerInput(t *testing.T, tariff economics.TariffSnapshot, observations []metering.Observation) economics.PostUsageRatingInput {
	t.Helper()
	in := v81SupplierInput(t, tariff, observations)
	in.Perspective = metering.PerspectiveCustomer
	in.Basis = economics.BasisCustomerPolicy
	in.Tariff = economics.RatingSnapshotRef{}
	in.TariffContent = nil
	in.Policy = economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "refinement81-policy", Version: "v1"}, PolicyID: "customer-independent"}
	in.PolicyContent = &economics.SnapshotContentRef{ContentRef: "catalog://refinement81/policy/v1", ContentHash: strings.Repeat("5", 64)}
	return in
}

//nolint:revive // test helper keeps t first per Go testing convention
func v81OpenFileStore(t *testing.T, ctx context.Context, path string) *DurableStore {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	seedTestSchemaIfEmpty(t, bunDB)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: v81StoreID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// v81AssertFullLine asserts the complete production-produced economic line
// tuple against explicit literal expectations: no local rating duplicate is
// used to compute anything.
func v81AssertFullLine(t *testing.T, v v81Vector, key metering.ComponentKey, ruleID, rate, expected string, obsID string, val economics.Valuation) {
	t.Helper()
	if len(val.Lines) != 1 {
		t.Fatalf("%s lines=%d, want 1", v.name, len(val.Lines))
	}
	line := val.Lines[0]
	if line.RuleID != ruleID {
		t.Fatalf("%s rule=%q, want %q", v.name, line.RuleID, ruleID)
	}
	if line.Status != economics.RatingLineRated {
		t.Fatalf("%s status=%q, want rated", v.name, line.Status)
	}
	if line.Component == nil || !line.Component.Equal(key) {
		t.Fatalf("%s component=%+v, want exact certified key", v.name, line.Component)
	}
	if line.Component.Direction != v.direction || line.Component.Component != v.component ||
		line.Component.Unit != v.unit || line.Component.SchemaID != "refinement81.cert.v1" {
		t.Fatalf("%s component identity=%+v", v.name, line.Component)
	}
	if len(line.Component.Dimensions) != len(v.dimensions) {
		t.Fatalf("%s dimensions=%+v", v.name, line.Component.Dimensions)
	}
	if line.Unit != v.unit {
		t.Fatalf("%s line unit=%q, want provider-native %q", v.name, line.Unit, v.unit)
	}
	if line.Quantity == nil || line.Quantity.CanonicalString() != v81Canonical(v.quantity) {
		t.Fatalf("%s quantity=%+v, want %q", v.name, line.Quantity, v81Canonical(v.quantity))
	}
	if line.UnitPrice == nil || line.UnitPrice.CanonicalString() != v81Canonical(rate) {
		t.Fatalf("%s unit price=%+v, want %q", v.name, line.UnitPrice, v81Canonical(rate))
	}
	if line.Amount == nil || line.Amount.CanonicalString() != v81Canonical(expected) {
		t.Fatalf("%s amount=%+v, want %q", v.name, line.Amount, v81Canonical(expected))
	}
	if len(val.Totals) != 1 || val.Totals[0].Currency != "USD" ||
		val.Totals[0].Amount == nil || val.Totals[0].Amount.CanonicalString() != v81Canonical(expected) {
		t.Fatalf("%s totals=%+v, want USD %q", v.name, val.Totals, v81Canonical(expected))
	}
	if val.Version != economics.ValuationVersionV2 || val.Completeness != economics.CompletenessComplete {
		t.Fatalf("%s valuation version/completeness=%d/%q", v.name, val.Version, val.Completeness)
	}
	if val.Scope != "call:refinement81" {
		t.Fatalf("%s scope=%q", v.name, val.Scope)
	}
	if val.Subject.Kind != metering.SubjectBLeg || val.Subject.StoreID != v81StoreID ||
		val.Subject.ALegID != "a-81" || val.Subject.BillingCallID != "call-81" || val.Subject.BLegID == "" {
		t.Fatalf("%s subject=%+v", v.name, val.Subject)
	}
	if val.ID == "" || val.Fingerprint() == "" {
		t.Fatalf("%s valuation lacks immutable identity", v.name)
	}
	if len(val.InputObservations) != 1 || val.InputObservations[0].ObservationID != obsID ||
		val.InputObservations[0].Revision != 1 || val.InputObservations[0].StoreID != v81StoreID ||
		val.InputObservations[0].PayloadHash == "" {
		t.Fatalf("%s input refs=%+v, want exact observation lineage", v.name, val.InputObservations)
	}
}

// TestRefinement81_ValuationDurability rates every 6.1 vector on the
// supplier (Q) and customer (R) planes, persists each production valuation
// through the real SQLite billingstore API, reopens the file-backed store,
// and proves the full economic line tuple survives durable readback. The
// two 6.2 transform vectors additionally carry their differing
// customer-boundary representation into the customer-plane rating to prove
// supplier/provider amounts are computed from the backend representation
// and survive durably.
func TestRefinement81_ValuationDurability(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ref81-valuations.db")
	store := v81OpenFileStore(t, ctx, path)

	providerRules := make([]economics.RatingRule, 0, 8)
	customerRules := make([]economics.RatingRule, 0, 8)
	keys := make(map[string]metering.ComponentKey, 8)
	for _, v := range v81Vectors() {
		key := v81Key(v)
		keys[v.name] = key
		providerRules = append(providerRules, v81LinearRule("v81-"+v.name+"-provider", key, v.providerRate))
		customerRules = append(customerRules, v81LinearRule("v81-"+v.name+"-customer", key, v.customerRate))
	}
	providerTariff := v81Tariff(t, "ref81-v-provider", providerRules)
	customerTariff := v81Tariff(t, "ref81-v-customer", customerRules)
	providerRater, err := billing.NewReferenceRater(providerTariff)
	if err != nil {
		t.Fatalf("NewReferenceRater provider: %v", err)
	}
	customerRater, err := billing.NewReferenceRater(customerTariff)
	if err != nil {
		t.Fatalf("NewReferenceRater customer: %v", err)
	}

	type persisted struct {
		vector      v81Vector
		key         metering.ComponentKey
		supplierVal economics.Valuation
		customerVal economics.Valuation
	}
	var persistedVals []persisted
	for _, v := range v81Vectors() {
		key := keys[v.name]
		bLeg := "b-81-v-" + v.name
		supplierObs := v81Observation(t, "v81-"+v.name+"-supplier", bLeg,
			metering.OriginProvider, v.backendBoundary, metering.PerspectiveOperator, key, v.quantity)

		supplierVal, err := providerRater.Rate(ctx, v81SupplierInput(t, providerTariff, []metering.Observation{supplierObs}))
		if err != nil {
			t.Fatalf("%s supplier Rate: %v", v.name, err)
		}
		if supplierVal.Basis != economics.BasisProviderQuantityLocal || supplierVal.Perspective != metering.PerspectiveOperator {
			t.Fatalf("%s supplier plane=%s/%s", v.name, supplierVal.Basis, supplierVal.Perspective)
		}
		if supplierVal.Tariff.ID != "ref81-v-provider" || supplierVal.Tariff.Version != "v1" {
			t.Fatalf("%s supplier tariff ref=%+v", v.name, supplierVal.Tariff)
		}
		v81AssertFullLine(t, v, key, "v81-"+v.name+"-provider", v.providerRate, v.expectedSupplier, supplierObs.ID, supplierVal)

		// Customer-plane inference rates the selected B-leg backend
		// quantity. For the two transform vectors the differing
		// customer-boundary representation rides along to prove it never
		// replaces provider inference usage.
		rObs := []metering.Observation{supplierObs}
		if v.name == "image-input" || v.name == "image-output" {
			var customerBoundary metering.Boundary
			var customerQty string
			if v.backendBoundary == metering.BoundaryBackendEgress {
				customerBoundary, customerQty = metering.BoundaryFrontendIngress, "9"
			} else {
				customerBoundary, customerQty = metering.BoundaryFrontendEgress, "9"
			}
			rObs = append(rObs, v81Observation(t, "v81-"+v.name+"-customer", bLeg,
				metering.OriginLocal, customerBoundary, metering.PerspectiveCustomer, key, customerQty))
		}
		customerVal, err := customerRater.Rate(ctx, v81CustomerInput(t, customerTariff, rObs))
		if err != nil {
			t.Fatalf("%s customer Rate: %v", v.name, err)
		}
		if customerVal.Basis != economics.BasisCustomerPolicy || customerVal.Perspective != metering.PerspectiveCustomer {
			t.Fatalf("%s customer plane=%s/%s", v.name, customerVal.Basis, customerVal.Perspective)
		}
		if customerVal.Policy.PolicyID != "customer-independent" {
			t.Fatalf("%s customer policy=%+v", v.name, customerVal.Policy)
		}
		v81AssertFullLine(t, v, key, "v81-"+v.name+"-customer", v.customerRate, v.expectedCustomer, supplierObs.ID, customerVal)

		for _, val := range []economics.Valuation{supplierVal, customerVal} {
			if err := store.AppendValuation(ctx, val); err != nil {
				t.Fatalf("%s AppendValuation(%s): %v", v.name, val.Basis, err)
			}
		}
		persistedVals = append(persistedVals, persisted{vector: v, key: key, supplierVal: supplierVal, customerVal: customerVal})
	}

	// Reopen from durable file state; readback uses only the new handle.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := v81OpenFileStore(t, ctx, path)

	for _, row := range persistedVals {
		row := row
		t.Run(row.vector.name, func(t *testing.T) {
			for _, want := range []struct {
				ruleID   string
				rate     string
				expected string
				original economics.Valuation
			}{
				{"v81-" + row.vector.name + "-provider", row.vector.providerRate, row.vector.expectedSupplier, row.supplierVal},
				{"v81-" + row.vector.name + "-customer", row.vector.customerRate, row.vector.expectedCustomer, row.customerVal},
			} {
				got, err := reopened.GetValuation(ctx, want.original.ID, want.original.Version)
				if err != nil {
					t.Fatalf("GetValuation(%q): %v", want.original.ID, err)
				}
				if err := got.Validate(); err != nil {
					t.Fatalf("readback Validate: %v", err)
				}
				if got.Fingerprint() != want.original.Fingerprint() {
					t.Fatalf("%s valuation fingerprint drift across durable reopen", want.ruleID)
				}
				v81AssertFullLine(t, row.vector, row.key, want.ruleID, want.rate, want.expected,
					want.original.InputObservations[0].ObservationID, got)
				if got.Basis != want.original.Basis || got.Perspective != want.original.Perspective ||
					got.Subject != want.original.Subject || got.Scope != want.original.Scope {
					t.Fatalf("%s envelope=%+v, want %+v", want.ruleID, got, want.original)
				}
			}
		})
	}
}
