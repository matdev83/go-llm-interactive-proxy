package billing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// C. A valid one-observation customer-policy payload with a deliberately wrong
// supplied input-set hash must fail with the typed mismatch sentinel instead
// of being silently replaced and rated.
func TestPhase19R1ProvenanceWrongSuppliedHashRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := phase9Key(metering.DirectionOutput, metering.ComponentOutputToken, metering.UnitToken)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("output-token", key, "0.03")})
	observation := phase9Observation(t, "r1c-obs", metering.OriginProvider, key, "200")
	input := phase9RatingInput(t, economics.BasisCustomerPolicy, []metering.Observation{observation}, tariff)
	input.Policy = economics.PolicySnapshotRef{
		VersionRef: economics.VersionRef{ID: "r1c-policy", Version: "v1"},
		PolicyID:   "r1c-policy",
	}
	input.PolicyContent = &economics.SnapshotContentRef{ContentRef: "catalog://policy/v1", ContentHash: strings.Repeat("3", 64)}
	ref, err := observation.Ref(observation.Subject.StoreID)
	if err != nil {
		t.Fatalf("observation ref: %v", err)
	}
	canonical, err := economics.CanonicalInputSetHash(economics.BasisCustomerPolicy, []metering.ObservationRef{ref})
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	// Control: the correct supplied hash rates successfully.
	input.InputSetHash = canonical
	if _, err := RateCustomerPolicyObservation(ctx, input, tariff); err != nil {
		t.Fatalf("valid supplied hash must rate: %v", err)
	}
	// A deliberately wrong nonempty hash must fail typed, not be erased.
	input.InputSetHash = strings.Repeat("0", 64)
	if input.InputSetHash == canonical {
		t.Fatal("wrong-hash fixture collides with canonical hash")
	}
	_, err = RateCustomerPolicyObservation(ctx, input, tariff)
	if !errors.Is(err, ErrInputSetHashMismatch) {
		t.Fatalf("wrong supplied hash error = %v, want errors.Is(ErrInputSetHashMismatch)", err)
	}
}

// D. A conflicting supplied observation reference payload hash must fail typed
// instead of being erased by selection filtering.
func TestPhase19R1ProvenanceConflictingSuppliedRefRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := phase9Key(metering.DirectionOutput, metering.ComponentOutputToken, metering.UnitToken)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("output-token", key, "0.03")})
	observation := phase9Observation(t, "r1d-obs", metering.OriginProvider, key, "200")
	input := phase9RatingInput(t, economics.BasisCustomerPolicy, []metering.Observation{observation}, tariff)
	input.Policy = economics.PolicySnapshotRef{
		VersionRef: economics.VersionRef{ID: "r1d-policy", Version: "v1"},
		PolicyID:   "r1d-policy",
	}
	input.PolicyContent = &economics.SnapshotContentRef{ContentRef: "catalog://policy/v1", ContentHash: strings.Repeat("3", 64)}
	ref, err := observation.Ref(observation.Subject.StoreID)
	if err != nil {
		t.Fatalf("observation ref: %v", err)
	}
	// Control: matching supplied refs rate successfully.
	input.ObservationRefs = []metering.ObservationRef{ref}
	if _, err := RateCustomerPolicyObservation(ctx, input, tariff); err != nil {
		t.Fatalf("matching supplied refs must rate: %v", err)
	}
	// Conflicting payload hash for the same observation identity must fail
	// typed instead of being silently dropped by selection.
	conflicting := ref
	conflicting.PayloadHash = strings.Repeat("f", 64)
	if conflicting.PayloadHash == ref.PayloadHash {
		t.Fatal("conflicting fixture collides with genuine payload hash")
	}
	input.ObservationRefs = []metering.ObservationRef{conflicting}
	_, err = RateCustomerPolicyObservation(ctx, input, tariff)
	if !errors.Is(err, ErrInputSetHashMismatch) {
		t.Fatalf("conflicting supplied ref error = %v, want errors.Is(ErrInputSetHashMismatch)", err)
	}
}
