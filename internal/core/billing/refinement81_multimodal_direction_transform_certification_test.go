package billing

// Task 8.1 multimodal direction and transform certification (requirements
// 6.1, 6.2). Test-only certification over existing production contracts:
// neutral V2 observations, ReferenceRater valuations and canonical JSON
// durable round-trip. No raw media is persisted and no production media
// codec, routing or rating code is changed by this file.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type cert81Vector struct {
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

func cert81Vectors() []cert81Vector {
	return []cert81Vector{
		{
			name: "image-input", component: metering.ComponentImage, direction: metering.DirectionInput,
			unit: metering.UnitImage,
			dimensions: []metering.Dimension{
				{Name: "quality", Value: "hd"},
				{Name: "transform", Value: "resize"},
			},
			quantity: "2", providerRate: "3", customerRate: "5",
			expectedSupplier: "6", expectedCustomer: "10",
			backendBoundary: metering.BoundaryBackendEgress,
		},
		{
			name: "image-output", component: metering.ComponentImage, direction: metering.DirectionOutput,
			unit: metering.UnitImage,
			dimensions: []metering.Dimension{
				{Name: "quality", Value: "hd"},
				{Name: "transform", Value: "transcode"},
			},
			quantity: "1", providerRate: "4", customerRate: "7",
			expectedSupplier: "4", expectedCustomer: "7",
			backendBoundary: metering.BoundaryBackendIngress,
		},
		{
			name: "audio-input-12.5s", component: metering.ComponentAudio, direction: metering.DirectionInput,
			unit: metering.UnitSecond,
			dimensions: []metering.Dimension{
				{Name: "codec", Value: "pcm"},
				{Name: "transform", Value: "trim"},
			},
			quantity: "12.5", providerRate: "0.5", customerRate: "2",
			expectedSupplier: "6.25", expectedCustomer: "25",
			backendBoundary: metering.BoundaryBackendEgress,
		},
		{
			name: "audio-output-8.0s", component: metering.ComponentAudio, direction: metering.DirectionOutput,
			unit: metering.UnitSecond,
			dimensions: []metering.Dimension{
				{Name: "codec", Value: "opus"},
				{Name: "transform", Value: "resample"},
			},
			quantity: "8.0", providerRate: "1", customerRate: "3",
			expectedSupplier: "8", expectedCustomer: "24",
			backendBoundary: metering.BoundaryBackendIngress,
		},
		{
			name: "video-input-provider-native-tokens", component: metering.ComponentVideo, direction: metering.DirectionInput,
			unit: metering.UnitToken,
			dimensions: []metering.Dimension{
				{Name: "tokenizer", Value: "provider_native"},
			},
			quantity: "4096", providerRate: "0.001", customerRate: "0.002",
			expectedSupplier: "4.096", expectedCustomer: "8.192",
			backendBoundary: metering.BoundaryBackendEgress,
		},
		{
			name: "video-output-generated-seconds", component: metering.ComponentVideo, direction: metering.DirectionOutput,
			unit: metering.UnitSecond,
			dimensions: []metering.Dimension{
				{Name: "generation", Value: "provider_generated"},
			},
			quantity: "8", providerRate: "2", customerRate: "5",
			expectedSupplier: "16", expectedCustomer: "40",
			backendBoundary: metering.BoundaryBackendIngress,
		},
		{
			name: "document-input-pages", component: metering.ComponentDocument, direction: metering.DirectionInput,
			unit: metering.UnitPage,
			dimensions: []metering.Dimension{
				{Name: "format", Value: "pdf"},
				{Name: "transform", Value: "extract"},
			},
			quantity: "4", providerRate: "1.5", customerRate: "2.5",
			expectedSupplier: "6", expectedCustomer: "10",
			backendBoundary: metering.BoundaryBackendEgress,
		},
		{
			name: "document-output-pages", component: metering.ComponentDocument, direction: metering.DirectionOutput,
			unit: metering.UnitPage,
			dimensions: []metering.Dimension{
				{Name: "format", Value: "pdf"},
				{Name: "transform", Value: "paginate"},
			},
			quantity: "2", providerRate: "3", customerRate: "6",
			expectedSupplier: "6", expectedCustomer: "12",
			backendBoundary: metering.BoundaryBackendIngress,
		},
	}
}

func cert81Key(v cert81Vector) metering.ComponentKey {
	return metering.ComponentKey{
		Direction:  v.direction,
		Component:  v.component,
		Unit:       v.unit,
		SchemaID:   "refinement81.cert.v1",
		Dimensions: append([]metering.Dimension(nil), v.dimensions...),
	}
}

func cert81Decimal(t *testing.T, raw string) *metering.Decimal {
	t.Helper()
	d, err := metering.ParseDecimal(raw)
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", raw, err)
	}
	return &d
}

func cert81Canonical(t *testing.T, raw string) string {
	t.Helper()
	return cert81Decimal(t, raw).CanonicalString()
}

// cert81Observation builds one neutral V2 B-leg observation. Supplier
// observations are provider-origin at backend boundaries; customer-boundary
// observations are local-origin at frontend boundaries with a deliberately
// different quantity so silent substitution is mechanically visible.
func cert81Observation(t *testing.T, id, bLegID string, origin string, boundary metering.Boundary, perspective metering.EconomicPerspective, key metering.ComponentKey, quantity string) metering.Observation {
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
			Kind: metering.SubjectBLeg, StoreID: "store-81",
			ALegID: "a-81", BillingCallID: "call-81", BLegID: bLegID,
		},
		Correlation: metering.CorrelationV2{
			StoreID: "store-81", ALegID: "a-81", BillingCallID: "call-81", BLegID: bLegID,
		},
		Semantics:  metering.SemanticsDelta,
		ObservedAt: now, ReceivedAt: now, MappingRef: "refinement81.cert.v1",
		Measures: []metering.Measure{{
			Key: key, Value: cert81Decimal(t, quantity), Quality: metering.QualityObserved,
			MethodRef: "refinement81.cert.v1",
		}},
	}
}

func cert81Tariff(t *testing.T, id string, rules []economics.RatingRule) economics.TariffSnapshot {
	t.Helper()
	return economics.NewTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: id, Version: "v1"}, RaterID: "reference"},
		"USD", rules,
	)
}

func cert81LinearRule(id string, key metering.ComponentKey, price string) economics.RatingRule {
	d, err := metering.ParseDecimal(price)
	if err != nil {
		panic(err)
	}
	return economics.RatingRule{ID: id, Component: &key, Currency: "USD", UnitPrice: &d}
}

func cert81SupplierInput(t *testing.T, tariff economics.TariffSnapshot, observations []metering.Observation) economics.PostUsageRatingInput {
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

func cert81CustomerInput(t *testing.T, tariff economics.TariffSnapshot, observations []metering.Observation) economics.PostUsageRatingInput {
	t.Helper()
	in := cert81SupplierInput(t, tariff, observations)
	in.Perspective = metering.PerspectiveCustomer
	in.Basis = economics.BasisCustomerPolicy
	in.Tariff = economics.RatingSnapshotRef{}
	in.TariffContent = nil
	in.Policy = economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "refinement81-policy", Version: "v1"}, PolicyID: "customer-independent"}
	in.PolicyContent = &economics.SnapshotContentRef{ContentRef: "catalog://refinement81/policy/v1", ContentHash: strings.Repeat("5", 64)}
	return in
}

// cert81AssertFullLine asserts the complete production-produced economic
// line tuple against explicit literal expectations: quantity and
// presence/completeness, provider-native unit, canonical component plus
// direction, schema ID and dimensions, rate ID/version and rate amount,
// computed line amount, native currency, and full subject/call/B-leg plus
// observation/valuation lineage. Nothing is recomputed with a local rating
// duplicate; expected values are literals and outputs are production rater
// results.
func cert81AssertFullLine(t *testing.T, label string, key metering.ComponentKey, ruleID, tariffID, qty, rate, expected, obsID, wantBLeg string, val economics.Valuation, wantBasis economics.ValuationBasis, wantPerspective metering.EconomicPerspective, wantPolicyID string) {
	t.Helper()
	if len(val.Lines) != 1 {
		t.Fatalf("%s lines=%d, want 1 direction-specific line", label, len(val.Lines))
	}
	line := val.Lines[0]
	if line.RuleID != ruleID {
		t.Fatalf("%s rule=%q, want %q", label, line.RuleID, ruleID)
	}
	if line.Status != economics.RatingLineRated {
		t.Fatalf("%s status=%q, want rated", label, line.Status)
	}
	if line.Component == nil || !line.Component.Equal(key) {
		t.Fatalf("%s component=%+v, want exact certified key", label, line.Component)
	}
	if line.Component.Direction != key.Direction || line.Component.Component != key.Component ||
		line.Component.Unit != key.Unit || line.Component.SchemaID != key.SchemaID {
		t.Fatalf("%s component identity=%+v", label, line.Component)
	}
	if len(line.Component.Dimensions) != len(key.Dimensions) {
		t.Fatalf("%s dimensions=%+v, want %+v", label, line.Component.Dimensions, key.Dimensions)
	}
	if line.Unit != key.Unit {
		t.Fatalf("%s line unit=%q, want provider-native %q", label, line.Unit, key.Unit)
	}
	if line.Quantity == nil || line.Quantity.CanonicalString() != cert81Canonical(t, qty) {
		t.Fatalf("%s quantity=%+v, want %q", label, line.Quantity, cert81Canonical(t, qty))
	}
	if line.UnitPrice == nil || line.UnitPrice.CanonicalString() != cert81Canonical(t, rate) {
		t.Fatalf("%s unit price=%+v, want frozen rate %q", label, line.UnitPrice, cert81Canonical(t, rate))
	}
	if line.Amount == nil || line.Amount.CanonicalString() != cert81Canonical(t, expected) {
		t.Fatalf("%s amount=%+v, want %q", label, line.Amount, cert81Canonical(t, expected))
	}
	if len(val.Totals) != 1 || val.Totals[0].Currency != "USD" ||
		val.Totals[0].Amount == nil || val.Totals[0].Amount.CanonicalString() != cert81Canonical(t, expected) {
		t.Fatalf("%s totals=%+v, want USD %q", label, val.Totals, cert81Canonical(t, expected))
	}
	if val.Version != economics.ValuationVersionV2 || val.Basis != wantBasis ||
		val.Perspective != wantPerspective || val.Completeness != economics.CompletenessComplete {
		t.Fatalf("%s envelope=%d/%s/%s/%s", label, val.Version, val.Basis, val.Perspective, val.Completeness)
	}
	if val.Scope != "call:refinement81" {
		t.Fatalf("%s scope=%q", label, val.Scope)
	}
	if val.Subject.Kind != metering.SubjectBLeg || val.Subject.StoreID != "store-81" ||
		val.Subject.ALegID != "a-81" || val.Subject.BillingCallID != "call-81" || val.Subject.BLegID != wantBLeg {
		t.Fatalf("%s subject=%+v", label, val.Subject)
	}
	if val.ID == "" || val.Fingerprint() == "" {
		t.Fatalf("%s valuation lacks immutable identity", label)
	}
	if val.Rater.ID != "refinement81-rater" || val.Rater.Version != "v1" {
		t.Fatalf("%s rater ref=%+v", label, val.Rater)
	}
	if val.Tariff.ID != tariffID || val.Tariff.Version != "v1" {
		t.Fatalf("%s tariff ref=%+v, want %q@v1", label, val.Tariff, tariffID)
	}
	if wantPolicyID != "" && val.Policy.PolicyID != wantPolicyID {
		t.Fatalf("%s policy=%+v, want %q", label, val.Policy, wantPolicyID)
	}
	if len(val.InputObservations) != 1 || val.InputObservations[0].ObservationID != obsID ||
		val.InputObservations[0].Revision != 1 || val.InputObservations[0].StoreID != "store-81" ||
		val.InputObservations[0].PayloadHash == "" {
		t.Fatalf("%s input refs=%+v, want exact observation lineage", label, val.InputObservations)
	}
}

// TestRefinement81_MultimodalDirectionRateCertification certifies requirement
// 6.1: every modality/direction vector rates independently through neutral
// evidence with provider-native units and direction-specific customer and
// provider prices. No vector converts media to text tokens.
func TestRefinement81_MultimodalDirectionRateCertification(t *testing.T) {
	t.Parallel()
	for _, v := range cert81Vectors() {
		v := v
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()
			key := cert81Key(v)
			if err := key.Validate(); err != nil {
				t.Fatalf("component key: %v", err)
			}
			supplierObs := cert81Observation(t, v.name+"-supplier", "b-81-"+v.name,
				metering.OriginProvider, v.backendBoundary, metering.PerspectiveOperator, key, v.quantity)
			if err := supplierObs.Validate(); err != nil {
				t.Fatalf("supplier observation: %v", err)
			}

			providerTariff := cert81Tariff(t, "refinement81-provider-"+v.name,
				[]economics.RatingRule{cert81LinearRule(v.name+"-provider", key, v.providerRate)})
			providerRater, err := NewReferenceRater(providerTariff)
			if err != nil {
				t.Fatalf("NewReferenceRater provider: %v", err)
			}
			supplierVal, err := providerRater.Rate(context.Background(),
				cert81SupplierInput(t, providerTariff, []metering.Observation{supplierObs}))
			if err != nil {
				t.Fatalf("supplier Rate: %v", err)
			}
			cert81AssertFullLine(t, v.name+"-supplier", key, v.name+"-provider",
				"refinement81-provider-"+v.name, v.quantity, v.providerRate, v.expectedSupplier,
				supplierObs.ID, "b-81-"+v.name, supplierVal,
				economics.BasisProviderQuantityLocal, metering.PerspectiveOperator, "")

			customerTariff := cert81Tariff(t, "refinement81-customer-"+v.name,
				[]economics.RatingRule{cert81LinearRule(v.name+"-customer", key, v.customerRate)})
			customerRater, err := NewReferenceRater(customerTariff)
			if err != nil {
				t.Fatalf("NewReferenceRater customer: %v", err)
			}
			customerVal, err := customerRater.Rate(context.Background(),
				cert81CustomerInput(t, customerTariff, []metering.Observation{supplierObs}))
			if err != nil {
				t.Fatalf("customer Rate: %v", err)
			}
			cert81AssertFullLine(t, v.name+"-customer", key, v.name+"-customer",
				"refinement81-customer-"+v.name, v.quantity, v.customerRate, v.expectedCustomer,
				supplierObs.ID, "b-81-"+v.name, customerVal,
				economics.BasisCustomerPolicy, metering.PerspectiveCustomer, "customer-independent")
			if supplierVal.Totals[0].Amount.CanonicalString() == customerVal.Totals[0].Amount.CanonicalString() {
				t.Fatalf("supplier and customer totals collapsed to %q; direction rates must be independently selectable",
					supplierVal.Totals[0].Amount.CanonicalString())
			}

			// No universal media-to-text-token conversion: a text-token-only
			// tariff must fail closed on media evidence.
			textKey := metering.ComponentKey{
				Direction: v.direction, Component: metering.ComponentTextToken,
				Unit: metering.UnitToken, SchemaID: "refinement81.cert.v1",
			}
			textTariff := cert81Tariff(t, "refinement81-textonly-"+v.name,
				[]economics.RatingRule{cert81LinearRule(v.name+"-text", textKey, "1")})
			textRater, err := NewReferenceRater(textTariff)
			if err != nil {
				t.Fatalf("NewReferenceRater text-only: %v", err)
			}
			origin := metering.OriginProvider
			basis := economics.BasisProviderQuantityLocal
			if _, err := textRater.Rate(context.Background(), economics.PostUsageRatingInput{
				Version: 2, Perspective: metering.PerspectiveOperator, Basis: basis,
				Subject: supplierObs.Subject, Scope: "call:refinement81",
				Observations: []metering.Observation{cert81Observation(t, v.name+"-textprobe", "b-81-"+v.name,
					origin, v.backendBoundary, metering.PerspectiveOperator, key, v.quantity)},
				Rater:                economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "refinement81-rater", Version: "v1"}, RaterID: "reference"},
				RaterContent:         &economics.SnapshotContentRef{ContentRef: "catalog://refinement81/rater/v1", ContentHash: strings.Repeat("1", 64)},
				Tariff:               textTariff.Ref,
				TariffContent:        &textTariff.Content,
				QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "catalog://refinement81/qualifiers/v1", ContentHash: strings.Repeat("4", 64)},
				AsOf:                 time.Unix(1_700_000_082, 0).UTC(),
			}); !errors.Is(err, ErrRateMissing) {
				t.Fatalf("text-token-only tariff error=%v, want %v", err, ErrRateMissing)
			}
		})
	}
}

// TestRefinement81_InputTransformUsesProviderBoundRepresentation certifies
// requirement 6.2 ingress direction: customer input resized before the
// provider. Supplier rating uses the final backend_egress representation;
// the distinct frontend_ingress diagnostic never replaces it.
func TestRefinement81_InputTransformUsesProviderBoundRepresentation(t *testing.T) {
	t.Parallel()
	key := metering.ComponentKey{
		Direction: metering.DirectionInput, Component: metering.ComponentImage,
		Unit: metering.UnitImage, SchemaID: "refinement81.cert.v1",
		Dimensions: []metering.Dimension{{Name: "transform", Value: "resize"}},
	}
	// Customer supplied 1 image; after frontend resize the provider-bound
	// representation is 2 images (bounded synthetic counts stand in for
	// resolution-derived billing units; no raw media is retained).
	providerObs := cert81Observation(t, "image-resize-backend", "b-81-resize",
		metering.OriginProvider, metering.BoundaryBackendEgress, metering.PerspectiveOperator, key, "2")
	customerObs := cert81Observation(t, "image-resize-customer", "b-81-resize",
		metering.OriginLocal, metering.BoundaryFrontendIngress, metering.PerspectiveCustomer, key, "1")

	tariff := cert81Tariff(t, "refinement81-resize-provider",
		[]economics.RatingRule{cert81LinearRule("image-resize", key, "3")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	got, err := rater.Rate(context.Background(),
		cert81SupplierInput(t, tariff, []metering.Observation{providerObs, customerObs}))
	if err != nil {
		t.Fatalf("supplier Rate with both boundaries: %v", err)
	}
	// Q plane selects provider-origin only, so the local customer_ingress
	// diagnostic is excluded and the total is 2*3=6, not 3*3=9.
	cert81AssertFullLine(t, "image-resize-supplier", key, "image-resize",
		"refinement81-resize-provider", "2", "3", "6",
		"image-resize-backend", "b-81-resize", got,
		economics.BasisProviderQuantityLocal, metering.PerspectiveOperator, "")
	for _, line := range got.Lines {
		for _, ref := range line.SourceObservationRefs {
			if ref.ObservationID == "image-resize-customer" {
				t.Fatalf("customer_ingress diagnostic leaked into supplier valuation: %+v", line)
			}
		}
	}
	// The supplier amount is computed from the backend-bound representation
	// alone: rating without the customer diagnostic must produce the same
	// rule, quantity, amount and single backend source ref.
	alone, err := rater.Rate(context.Background(),
		cert81SupplierInput(t, tariff, []metering.Observation{providerObs}))
	if err != nil {
		t.Fatalf("supplier Rate without customer diagnostic: %v", err)
	}
	if alone.Lines[0].Amount.CanonicalString() != got.Lines[0].Amount.CanonicalString() ||
		alone.Lines[0].RuleID != got.Lines[0].RuleID ||
		alone.Lines[0].Quantity.CanonicalString() != got.Lines[0].Quantity.CanonicalString() ||
		len(alone.Lines[0].SourceObservationRefs) != 1 {
		t.Fatalf("supplier line changed with customer representation present: alone=%+v both=%+v",
			alone.Lines[0], got.Lines[0])
	}

	customerTariff := cert81Tariff(t, "refinement81-resize-customer",
		[]economics.RatingRule{cert81LinearRule("image-resize-customer", key, "5")})
	customerRater, err := NewReferenceRater(customerTariff)
	if err != nil {
		t.Fatalf("NewReferenceRater customer: %v", err)
	}
	customerVal, err := customerRater.Rate(context.Background(),
		cert81CustomerInput(t, customerTariff, []metering.Observation{providerObs, customerObs}))
	if err != nil {
		t.Fatalf("customer Rate with both boundaries: %v", err)
	}
	// R plane admits only backend boundaries, so inference rates the
	// provider-bound 2 images at the customer price: 2*5=10.
	cert81AssertFullLine(t, "image-resize-customer", key, "image-resize-customer",
		"refinement81-resize-customer", "2", "5", "10",
		"image-resize-backend", "b-81-resize", customerVal,
		economics.BasisCustomerPolicy, metering.PerspectiveCustomer, "customer-independent")
}

// TestRefinement81_OutputTransformPreservesProviderOrigin certifies
// requirement 6.2 egress direction: provider image output transcoded before
// the client. Supplier rating remains provider-origin (backend_ingress);
// the customer-visible frontend_egress representation stays separate.
func TestRefinement81_OutputTransformPreservesProviderOrigin(t *testing.T) {
	t.Parallel()
	key := metering.ComponentKey{
		Direction: metering.DirectionOutput, Component: metering.ComponentImage,
		Unit: metering.UnitImage, SchemaID: "refinement81.cert.v1",
		Dimensions: []metering.Dimension{{Name: "transform", Value: "transcode"}},
	}
	providerObs := cert81Observation(t, "image-transcode-backend", "b-81-transcode",
		metering.OriginProvider, metering.BoundaryBackendIngress, metering.PerspectiveOperator, key, "3")
	customerObs := cert81Observation(t, "image-transcode-customer", "b-81-transcode",
		metering.OriginLocal, metering.BoundaryFrontendEgress, metering.PerspectiveCustomer, key, "1")

	tariff := cert81Tariff(t, "refinement81-transcode-provider",
		[]economics.RatingRule{cert81LinearRule("image-transcode", key, "4")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	got, err := rater.Rate(context.Background(),
		cert81SupplierInput(t, tariff, []metering.Observation{providerObs, customerObs}))
	if err != nil {
		t.Fatalf("supplier Rate with both boundaries: %v", err)
	}
	// 3 provider-origin images at 4 each = 12; the transcoded customer
	// 1 image must not reduce supplier economics.
	cert81AssertFullLine(t, "image-transcode-supplier", key, "image-transcode",
		"refinement81-transcode-provider", "3", "4", "12",
		"image-transcode-backend", "b-81-transcode", got,
		economics.BasisProviderQuantityLocal, metering.PerspectiveOperator, "")
	alone, err := rater.Rate(context.Background(),
		cert81SupplierInput(t, tariff, []metering.Observation{providerObs}))
	if err != nil {
		t.Fatalf("supplier Rate without customer representation: %v", err)
	}
	if alone.Lines[0].Amount.CanonicalString() != got.Lines[0].Amount.CanonicalString() ||
		alone.Lines[0].RuleID != got.Lines[0].RuleID ||
		alone.Lines[0].Quantity.CanonicalString() != got.Lines[0].Quantity.CanonicalString() ||
		len(alone.Lines[0].SourceObservationRefs) != 1 {
		t.Fatalf("supplier line changed with customer representation present: alone=%+v both=%+v",
			alone.Lines[0], got.Lines[0])
	}

	customerTariff := cert81Tariff(t, "refinement81-transcode-customer",
		[]economics.RatingRule{cert81LinearRule("image-transcode-customer", key, "7")})
	customerRater, err := NewReferenceRater(customerTariff)
	if err != nil {
		t.Fatalf("NewReferenceRater customer: %v", err)
	}
	customerVal, err := customerRater.Rate(context.Background(),
		cert81CustomerInput(t, customerTariff, []metering.Observation{providerObs, customerObs}))
	if err != nil {
		t.Fatalf("customer Rate with both boundaries: %v", err)
	}
	// Customer inference still rates the provider-origin 3 images at the
	// customer price: 3*7=21. The frontend_egress 1 image is diagnostic
	// service evidence, never a silent substitute.
	cert81AssertFullLine(t, "image-transcode-customer", key, "image-transcode-customer",
		"refinement81-transcode-customer", "3", "7", "21",
		"image-transcode-backend", "b-81-transcode", customerVal,
		economics.BasisCustomerPolicy, metering.PerspectiveCustomer, "customer-independent")
}

// TestRefinement81_DurableRoundTripPreservesMultimodalIdentity proves neutral
// evidence -> valuation -> durable JSON round-trip preserves component,
// direction, unit, schema/dimensions, quantity, subject and immutable
// references exactly for every modality/direction in scope.
func TestRefinement81_DurableRoundTripPreservesMultimodalIdentity(t *testing.T) {
	t.Parallel()
	for _, v := range cert81Vectors() {
		v := v
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()
			key := cert81Key(v)
			obs := cert81Observation(t, v.name+"-durable", "b-81-"+v.name,
				metering.OriginProvider, v.backendBoundary, metering.PerspectiveOperator, key, v.quantity)
			wire, err := obs.CanonicalJSON()
			if err != nil {
				t.Fatalf("CanonicalJSON: %v", err)
			}
			var decoded metering.Observation
			if err := json.Unmarshal(wire, &decoded); err != nil {
				t.Fatalf("unmarshal observation: %v", err)
			}
			if err := decoded.Validate(); err != nil {
				t.Fatalf("decoded Validate: %v", err)
			}
			if decoded.Fingerprint() != obs.Fingerprint() {
				t.Fatal("observation fingerprint changed across durable round-trip")
			}
			if len(decoded.Measures) != 1 || !decoded.Measures[0].Key.Equal(key) {
				t.Fatalf("measure key changed: %+v", decoded.Measures)
			}
			if decoded.Measures[0].Value == nil || decoded.Measures[0].Value.CanonicalString() != cert81Canonical(t, v.quantity) {
				t.Fatalf("quantity changed: %+v", decoded.Measures[0].Value)
			}
			if decoded.Subject != obs.Subject || decoded.Correlation.BLegID != obs.Correlation.BLegID ||
				decoded.Boundary != obs.Boundary || decoded.Perspective != obs.Perspective {
				t.Fatalf("subject/boundary/perspective changed: %+v", decoded)
			}
			if strings.Contains(string(wire), "data:image") || strings.Contains(string(wire), "base64") {
				t.Fatal("durable observation retains raw media payload")
			}

			tariff := cert81Tariff(t, "refinement81-durable-"+v.name,
				[]economics.RatingRule{cert81LinearRule(v.name+"-durable", key, v.providerRate)})
			rater, err := NewReferenceRater(tariff)
			if err != nil {
				t.Fatalf("NewReferenceRater: %v", err)
			}
			val, err := rater.Rate(context.Background(),
				cert81SupplierInput(t, tariff, []metering.Observation{decoded}))
			if err != nil {
				t.Fatalf("Rate decoded observation: %v", err)
			}
			valWire, err := val.CanonicalJSON()
			if err != nil {
				t.Fatalf("valuation CanonicalJSON: %v", err)
			}
			var decodedVal economics.Valuation
			if err := json.Unmarshal(valWire, &decodedVal); err != nil {
				t.Fatalf("unmarshal valuation: %v", err)
			}
			if err := decodedVal.Validate(); err != nil {
				t.Fatalf("decoded valuation Validate: %v", err)
			}
			if decodedVal.Fingerprint() != val.Fingerprint() {
				t.Fatal("valuation fingerprint changed across durable round-trip")
			}
			if len(decodedVal.Lines) != 1 || decodedVal.Lines[0].Component == nil ||
				!decodedVal.Lines[0].Component.Equal(key) {
				t.Fatalf("valuation line component changed: %+v", decodedVal.Lines)
			}
			if len(decodedVal.InputObservations) != 1 {
				t.Fatalf("valuation refs changed: %+v", decodedVal.InputObservations)
			}
		})
	}
}

// TestRefinement81_MissingDetailNeverBecomesZeroOrTextTokens proves missing
// multimodal detail stays missing/partial: never zero and never a
// text-token equivalent.
func TestRefinement81_MissingDetailNeverBecomesZeroOrTextTokens(t *testing.T) {
	t.Parallel()
	for _, v := range cert81Vectors() {
		v := v
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()
			key := cert81Key(v)
			obs := cert81Observation(t, v.name+"-missing", "b-81-"+v.name,
				metering.OriginProvider, v.backendBoundary, metering.PerspectiveOperator, key, v.quantity)
			obs.Measures[0].Value = nil
			obs.Measures[0].Quality = metering.QualityUnavailable
			if err := obs.Validate(); err != nil {
				t.Fatalf("unavailable observation must validate: %v", err)
			}
			tariff := cert81Tariff(t, "refinement81-missing-"+v.name,
				[]economics.RatingRule{cert81LinearRule(v.name+"-missing", key, v.providerRate)})
			rater, err := NewReferenceRater(tariff)
			if err != nil {
				t.Fatalf("NewReferenceRater: %v", err)
			}
			_, err = rater.Rate(context.Background(),
				cert81SupplierInput(t, tariff, []metering.Observation{obs}))
			if !errors.Is(err, ErrQuantityIncomplete) {
				t.Fatalf("missing quantity error=%v, want %v (never zero)", err, ErrQuantityIncomplete)
			}
			if err != nil && strings.Contains(strings.ToLower(err.Error()), "text") &&
				v.component != metering.ComponentTextToken {
				t.Fatalf("missing media detail coerced toward text tokens: %v", err)
			}
		})
	}
}

// TestRefinement81_DirectionIsCanonicalIdentity proves direction is part of
// the canonical component fingerprint: input and output can never merge,
// and non-directional scope stays in subject identity rather than a fake
// flow direction.
func TestRefinement81_DirectionIsCanonicalIdentity(t *testing.T) {
	t.Parallel()
	pairs := []struct {
		component string
		unit      string
	}{
		{metering.ComponentImage, metering.UnitImage},
		{metering.ComponentAudio, metering.UnitSecond},
		{metering.ComponentVideo, metering.UnitSecond},
		{metering.ComponentDocument, metering.UnitPage},
	}
	for _, p := range pairs {
		p := p
		t.Run(p.component, func(t *testing.T) {
			t.Parallel()
			in := metering.ComponentKey{
				Direction: metering.DirectionInput, Component: p.component,
				Unit: p.unit, SchemaID: "refinement81.cert.v1",
			}
			out := in
			out.Direction = metering.DirectionOutput
			if in.Equal(out) || in.CanonicalKey() == out.CanonicalKey() || in.Fingerprint() == out.Fingerprint() {
				t.Fatalf("%s input/output keys merged", p.component)
			}
			// Video preserves native units per direction: tokens in,
			// seconds out. The distinct units are part of identity.
			if p.component == metering.ComponentVideo {
				tokens := metering.ComponentKey{
					Direction: metering.DirectionInput, Component: metering.ComponentVideo,
					Unit: metering.UnitToken, SchemaID: "refinement81.cert.v1",
				}
				seconds := metering.ComponentKey{
					Direction: metering.DirectionOutput, Component: metering.ComponentVideo,
					Unit: metering.UnitSecond, SchemaID: "refinement81.cert.v1",
				}
				if tokens.Equal(seconds) || tokens.CanonicalKey() == seconds.CanonicalKey() {
					t.Fatal("video input tokens and output seconds merged")
				}
			}
		})
	}
	if err := (metering.ComponentKey{
		Direction: metering.FlowDirection("request"), Component: metering.ComponentStorage,
		Unit: metering.UnitByteSecond,
	}).Validate(); err == nil {
		t.Fatal("request/resource scope must not become a flow direction")
	}
}
