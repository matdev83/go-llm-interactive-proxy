package economics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// These constants were independently assembled from the pre-advisory wire
// structs and hashed with OpenSSL. Keep them literal: deriving expected bytes
// from Canonical, CanonicalContextJSON, or Fingerprint would make this a
// self-comparison instead of a compatibility pin.
const (
	legacyTariffHashPreimage = `{"ref":{"id":"legacy-tariff","version":"v1","rater_id":"legacy-rater"},"currency":"USD","rules":[]}`
	legacyTariffContentHash  = "cf6daa0b7664389a6a50b8f53ba87d03aac45571bf12498443880e291cb98fca"
	legacyTariffSnapshotJSON = `{"ref":{"id":"legacy-tariff","version":"v1","rater_id":"legacy-rater"},"currency":"USD","rules":[],"content":{"content_ref":"tariff-snapshot:v1://legacy-tariff/v1","content_hash":"cf6daa0b7664389a6a50b8f53ba87d03aac45571bf12498443880e291cb98fca"}}`
	legacyCatalogViewJSON    = `{"currency":"USD"}`

	legacyValuationContextJSON = `{"version":2,"perspective":"operator","basis":"provider_reported","subject":{"kind":"b_leg","store_id":"legacy-store","b_leg_id":"legacy-b-leg"},"scope":"legacy-scope","payer":{"kind":"customer","id":"legacy-customer"},"rater":{"id":"","version":"","rater_id":"","policy_id":"","content":{"content_ref":"","content_hash":""}},"tariff":{"id":"","version":"","rater_id":"","policy_id":"","content":{"content_ref":"","content_hash":""}},"policy":{"id":"","version":"","rater_id":"","policy_id":"","content":{"content_ref":"","content_hash":""}},"qualifier_snapshot":"","qualifier_snapshot_ref":{"content_ref":"","content_hash":""}}`
	legacyValuationContextHash = "d067c95772f830ada766a6ecc8e6888b4abbfdc3f989f3567769626710be4779"
	legacyValuationJSON        = `{"id":"legacy-valuation","version":2,"perspective":"operator","basis":"provider_reported","subject":{"kind":"b_leg","store_id":"legacy-store","b_leg_id":"legacy-b-leg"},"scope":"legacy-scope","input_observations":[{"store_id":"legacy-store","observation_id":"legacy-observation","revision":1,"payload_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],"rater":{"id":"","version":""},"tariff":{"id":"","version":""},"policy":{"id":"","version":""},"payer":{"kind":"customer","id":"legacy-customer"},"lines":null,"totals":null,"completeness":"partial","created_at":"2025-01-02T03:04:05Z"}`
	legacyValuationFingerprint = "6082cf7892aa43db7950f7413701848684447a9f4454da8da40a4d9be3d7cafa"
)

func TestPreAdvisorySnapshotAndCatalogViewIdentityGolden(t *testing.T) {
	view := RatingCatalogView{Currency: "USD"}
	viewBytes, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal legacy rating catalog view: %v", err)
	}
	if got := string(viewBytes); got != legacyCatalogViewJSON {
		t.Fatalf("legacy catalog view JSON = %s, want %s", got, legacyCatalogViewJSON)
	}

	ref := RatingSnapshotRef{VersionRef: VersionRef{ID: "legacy-tariff", Version: "v1"}, RaterID: "legacy-rater"}
	snapshot, err := view.Tariff(ref)
	if err != nil {
		t.Fatalf("materialize legacy tariff: %v", err)
	}
	canonical, err := snapshot.Canonical()
	if err != nil {
		t.Fatalf("canonicalize legacy tariff: %v", err)
	}
	snapshotBytes, err := json.Marshal(canonical)
	if err != nil {
		t.Fatalf("marshal canonical legacy tariff: %v", err)
	}
	if got := string(snapshotBytes); got != legacyTariffSnapshotJSON {
		t.Fatalf("legacy tariff JSON = %s, want %s", got, legacyTariffSnapshotJSON)
	}
	if got := snapshot.ContentHash(); got != legacyTariffContentHash {
		t.Fatalf("legacy tariff content hash = %s, want %s", got, legacyTariffContentHash)
	}
	assertIndependentSHA256(t, legacyTariffHashPreimage, legacyTariffContentHash)
}

func TestPreAdvisoryValuationContextAndFingerprintGolden(t *testing.T) {
	valuation := preAdvisoryValuationFixture()

	contextBytes, err := valuation.CanonicalContextJSON()
	if err != nil {
		t.Fatalf("canonical valuation context: %v", err)
	}
	if got := string(contextBytes); got != legacyValuationContextJSON {
		t.Fatalf("legacy valuation context JSON = %s, want %s", got, legacyValuationContextJSON)
	}
	if got := valuation.ContextHash(); got != legacyValuationContextHash {
		t.Fatalf("legacy valuation context hash = %s, want %s", got, legacyValuationContextHash)
	}
	assertIndependentSHA256(t, legacyValuationContextJSON, legacyValuationContextHash)

	valuationBytes, err := valuation.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical legacy valuation: %v", err)
	}
	if got := string(valuationBytes); got != legacyValuationJSON {
		t.Fatalf("legacy valuation JSON = %s, want %s", got, legacyValuationJSON)
	}
	if got := valuation.Fingerprint(); got != legacyValuationFingerprint {
		t.Fatalf("legacy valuation fingerprint = %s, want %s", got, legacyValuationFingerprint)
	}
	assertIndependentSHA256(t, legacyValuationJSON, legacyValuationFingerprint)
}

func preAdvisoryValuationFixture() Valuation {
	return Valuation{
		ID: "legacy-valuation", Version: ValuationVersionV2,
		Perspective: metering.PerspectiveOperator, Basis: BasisProviderReported,
		Subject: metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "legacy-store", BLegID: "legacy-b-leg"},
		Scope:   "legacy-scope",
		InputObservations: []metering.ObservationRef{{
			StoreID: "legacy-store", ObservationID: "legacy-observation", Revision: 1,
			PayloadHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
		Payer:        metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "legacy-customer"},
		Completeness: CompletenessPartial,
		CreatedAt:    time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC),
	}
}

func assertIndependentSHA256(t *testing.T, preimage, want string) {
	t.Helper()
	sum := sha256.Sum256([]byte(preimage))
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("independent fixture preimage hashes to %s, want %s", got, want)
	}
}
