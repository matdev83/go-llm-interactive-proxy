package feature_test

import (
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

func TestLegalPipelineStageIDs_countAndOrder(t *testing.T) {
	t.Parallel()
	ids := feature.LegalPipelineStageIDs()
	if len(ids) != 14 {
		t.Fatalf("want 14 stages, got %d", len(ids))
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
	if len(desc) != 14 {
		t.Fatalf("descriptors: want 14 got %d", len(desc))
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

func TestLegalPipelineStageIDs_secretGuardBetweenSessionOpenAndSubmit(t *testing.T) {
	t.Parallel()
	openIdx := feature.LegalStageDescriptorIndex(feature.StageIDSessionOpen)
	guardIdx := feature.LegalStageDescriptorIndex(feature.StageIDSecretGuard)
	submitIdx := feature.LegalStageDescriptorIndex(feature.StageIDSubmit)
	if !(openIdx < guardIdx && guardIdx < submitIdx) {
		t.Fatalf("want session_open(%d) < secret_guard(%d) < submit_request(%d)", openIdx, guardIdx, submitIdx)
	}
	desc, ok := feature.StageDescriptorByID(feature.StageIDSecretGuard)
	if !ok || desc.MutationRole != feature.StageRoleMutateReject {
		t.Fatalf("secret_guard descriptor: ok=%v role=%v", ok, desc.MutationRole)
	}
}

func TestStageMutationRole_zeroIsUnknown(t *testing.T) {
	t.Parallel()
	var z feature.StageDescriptor
	if z.MutationRole != feature.StageRoleUnknown {
		t.Fatalf("zero descriptor mutation role: %v", z.MutationRole)
	}
}
