package feature_test

import (
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

func TestLegalPipelineStageIDs_countAndOrder(t *testing.T) {
	t.Parallel()
	ids := feature.LegalPipelineStageIDs()
	if len(ids) != 12 {
		t.Fatalf("want 12 stages, got %d", len(ids))
	}
	seen := map[string]struct{}{}
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate stage id %q", id)
		}
		seen[id] = struct{}{}
		if !feature.ValidateStageID(id) {
			t.Fatalf("ValidateStageID(%q) false", id)
		}
	}
	desc := feature.LegalStageDescriptors()
	if len(desc) != 12 {
		t.Fatalf("descriptors: want 12 got %d", len(desc))
	}
	gotDescIDs := make([]string, len(desc))
	for i := range desc {
		gotDescIDs[i] = desc[i].ID
	}
	if !slices.Equal(ids, gotDescIDs) {
		t.Fatalf("descriptor order != pipeline ids\ngot  %#v\nwant %#v", gotDescIDs, ids)
	}
}

func TestStageDescriptorByID_unknown(t *testing.T) {
	t.Parallel()
	_, ok := feature.StageDescriptorByID("not_a_real_stage")
	if ok {
		t.Fatal("expected false for unknown stage")
	}
}

func TestStageMutationRole_zeroIsUnknown(t *testing.T) {
	t.Parallel()
	var z feature.StageDescriptor
	if z.MutationRole != feature.StageRoleUnknown {
		t.Fatalf("zero descriptor mutation role: %v", z.MutationRole)
	}
}
