package feature_test

import (
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
)

type stubAttemptTransform struct {
	id  string
	ord int
}

func (s stubAttemptTransform) ID() string                        { return s.id }
func (s stubAttemptTransform) Order() int                        { return s.ord }
func (s stubAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s stubAttemptTransform) HandleAttempt(context.Context, *lipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

func TestFeatureBundle_AttemptTransforms_requiresSchemaV1(t *testing.T) {
	t.Parallel()
	only := feature.FeatureBundle{
		AttemptTransforms: []request.AttemptTransform{stubAttemptTransform{id: "at", ord: 0}},
	}
	if err := only.Validate(); err == nil {
		t.Fatal("non-empty AttemptTransforms with schema 0 must fail Validate")
	}
	ok := feature.FeatureBundle{
		SchemaVersion:     feature.SchemaVersionV1,
		AttemptTransforms: []request.AttemptTransform{stubAttemptTransform{id: "at", ord: 0}},
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureBundle_AttemptTransforms_rejectsNilEntry(t *testing.T) {
	t.Parallel()
	b := feature.FeatureBundle{
		SchemaVersion:     feature.SchemaVersionV1,
		AttemptTransforms: []request.AttemptTransform{nil},
	}
	if err := b.Validate(); err == nil {
		t.Fatal("nil AttemptTransforms entry must fail Validate")
	}
}

func TestFeatureBundle_AttemptTransforms_omittedRemainsNoOp(t *testing.T) {
	t.Parallel()
	var b feature.FeatureBundle
	if b.AttemptTransforms != nil {
		t.Fatal("omitted AttemptTransforms must be nil on zero value")
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	v1Empty := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1}
	if err := v1Empty.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLegalPipeline_candidateAttemptTransformBetweenRouteHintAndAttemptLifecycle(t *testing.T) {
	t.Parallel()
	routeIdx := feature.LegalStageDescriptorIndex(feature.StageIDRouteHinting)
	xformIdx := feature.LegalStageDescriptorIndex(feature.StageIDCandidateAttemptTransform)
	lifeIdx := feature.LegalStageDescriptorIndex(feature.StageIDAttemptLifecycle)
	if routeIdx < 0 || xformIdx < 0 || lifeIdx < 0 {
		t.Fatalf("missing stages route=%d transform=%d lifecycle=%d", routeIdx, xformIdx, lifeIdx)
	}
	if routeIdx >= xformIdx || xformIdx >= lifeIdx {
		t.Fatalf("want route_hinting(%d) < candidate_attempt_transform(%d) < attempt_lifecycle(%d)", routeIdx, xformIdx, lifeIdx)
	}
	desc, ok := feature.StageDescriptorByID(feature.StageIDCandidateAttemptTransform)
	if !ok || desc.MutationRole != feature.StageRoleMutateReject {
		t.Fatalf("candidate_attempt_transform descriptor: ok=%v role=%v", ok, desc.MutationRole)
	}
	life, ok := feature.StageDescriptorByID(feature.StageIDAttemptLifecycle)
	if !ok || life.MutationRole != feature.StageRoleObserve {
		t.Fatalf("attempt_lifecycle must remain observe-only: ok=%v role=%v", ok, life.MutationRole)
	}
}

func TestFreezeRequestPlanes_panicsOnNilAttemptTransform(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("want panic on nil AttemptTransform")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "AttemptTransforms contains nil entry") {
			t.Fatalf("panic=%v", r)
		}
	}()
	frozen := feature.NewMalformedGeneratedFrozenCandidateForTest(nil, []request.AttemptTransform{nil})
	_ = feature.FreezeRequestPlanes(frozen)
}
