package metering

import (
	_ "embed"
	"encoding/json"
	"testing"
)

//go:embed testdata/refinement_phase1_multimodal_vectors.json
var refinementPhase1MultimodalVectors []byte

type refinementPhase1Vector struct {
	ID            string            `json:"id"`
	Media         string            `json:"media"`
	Direction     string            `json:"direction"`
	Unit          string            `json:"unit"`
	ProviderValue string            `json:"provider_value"`
	CustomerValue string            `json:"customer_value"`
	Qualifiers    map[string]string `json:"qualifiers"`
	BillingCallID string            `json:"billing_call_id"`
	ALegID        string            `json:"a_leg_id"`
	BLegIDs       []string          `json:"b_leg_ids"`
	Terminal      bool              `json:"terminal"`
	Revision      int               `json:"revision"`
	Supersedes    []string          `json:"supersedes"`
}

// TestRefinementPhase1MultimodalFixtureInventory is an inventory-only fixture
// check. It makes the Phase 1 acceptance vectors concrete before the V2 DTOs
// exist and rejects accidental token coercion of native media units. It does
// not certify economic behavior: parent Tasks 2.1-2.5, 3.1-3.4 and 4.1-4.2
// must provide the V2 identity, normalization and durable revision seams
// before these vectors can execute against production contracts.
func TestRefinementPhase1MultimodalFixtureInventory(t *testing.T) {
	t.Parallel()

	var vectors []refinementPhase1Vector
	if err := json.Unmarshal(refinementPhase1MultimodalVectors, &vectors); err != nil {
		t.Fatalf("decode multimodal fixture: %v", err)
	}
	want := map[string]struct {
		media     string
		direction string
		unit      string
	}{
		"image-input-provider-bound-resize":      {media: "image", direction: "input", unit: "image"},
		"image-output-provider-origin-transcode": {media: "image", direction: "output", unit: "image"},
		"audio-input-provider-bound-duration":    {media: "audio", direction: "input", unit: "second"},
		"audio-output-provider-origin-duration":  {media: "audio", direction: "output", unit: "second"},
		"video-input-provider-bound-frames":      {media: "video", direction: "input", unit: "frame"},
		"video-output-provider-origin-frames":    {media: "video", direction: "output", unit: "frame"},
		"document-input-provider-bound-pages":    {media: "document", direction: "input", unit: "page"},
		"document-output-provider-origin-pages":  {media: "document", direction: "output", unit: "page"},
		"multimodal-derived-aggregate":           {media: "mixed", direction: "none", unit: "aggregate"},
		"same-a-leg-resumed-billing-call":        {media: "continuation", direction: "none", unit: "call"},
		"preterminal-revision":                   {media: "audio", direction: "output", unit: "second"},
		"late-correction-after-terminal":         {media: "audio", direction: "output", unit: "second"},
	}
	got := make(map[string]struct{}, len(vectors))
	for _, vector := range vectors {
		if _, duplicate := got[vector.ID]; duplicate {
			t.Fatalf("duplicate fixture vector %q", vector.ID)
		}
		got[vector.ID] = struct{}{}
		expect, ok := want[vector.ID]
		if !ok {
			t.Fatalf("unexpected fixture vector %q", vector.ID)
		}
		if vector.Media != expect.media || vector.Direction != expect.direction || vector.Unit != expect.unit {
			t.Fatalf("vector %q identity = (%q, %q, %q), want (%q, %q, %q)", vector.ID, vector.Media, vector.Direction, vector.Unit, expect.media, expect.direction, expect.unit)
		}
		if vector.Unit == UnitToken {
			t.Fatalf("vector %q coerced native media unit to token", vector.ID)
		}
		if vector.ProviderValue == "" || vector.CustomerValue == "" || len(vector.Qualifiers) == 0 {
			t.Fatalf("vector %q lacks provider/customer values or transform qualifiers", vector.ID)
		}
		if vector.BillingCallID == "" || vector.ALegID == "" || len(vector.BLegIDs) == 0 || vector.Revision < 1 {
			t.Fatalf("vector %q lacks call/leg lineage or revision", vector.ID)
		}
		if vector.ID == "late-correction-after-terminal" && len(vector.Supersedes) != 1 {
			t.Fatalf("late correction %q must supersede exactly one preterminal observation", vector.ID)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("fixture vectors = %d, want %d", len(got), len(want))
	}
}
