package runtimebundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/runtimegen"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type phase53Req struct{ id string }

func (s phase53Req) Describe() authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID: s.id, Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage: authority.StageRequestAdmit, Strength: authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func (s phase53Req) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (s phase53Req) SettleRequest(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
	return authority.Settlement{}, nil
}
func (s phase53Req) ReleaseRequest(context.Context, authority.RequestRelease) error { return nil }

type phase53Rater struct{ id string }

func (s phase53Rater) Rate(context.Context, economics.RatingRequest) (economics.RatingResult, error) {
	return economics.RatingResult{RaterID: s.id}, nil
}

func TestPhase53_CompileStaticContributionAndPublishExecutable(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Accounting.Concurrency.Enabled = true
	cfg.Accounting.Concurrency.SnapshotVersion = "conc-v2"
	cfg.Accounting.Concurrency.Rules = []config.ConcurrencyAuthorityRuleConfig{{
		ID: "max-active", MaxActiveRequests: 2,
		Match: config.AccountingAuthorityDimensionsConfig{
			Principal: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("alice")},
		},
	}}
	prod := runtimebundle.ProductionOptions{
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: phase53Req{id: "quota"}.Describe(),
			Priority:   authority.RequestPriorityQuotaBudgetRate,
			Provider:   phase53Req{id: "quota"},
		}},
		RaterRegistrations: []economics.RaterRegistration{{
			ID: "op-rater", Perspective: metering.PerspectiveOperator, Rater: phase53Rater{id: "op-rater"},
		}},
	}
	contrib := runtimebundle.CompileGenerationContribution(cfg, prod, time.Unix(10, 0).UTC())
	if err := contrib.Validate(); err != nil {
		t.Fatal(err)
	}
	if contrib.MaxActiveRequests != 2 || contrib.SourceID != "static-config" {
		t.Fatalf("contrib=%+v", contrib)
	}
	pub := snapshotgen.NewPublisher()
	gen, err := runtimebundle.PublishExecutableFromProduction(pub, cfg, prod, time.Unix(10, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if gen.EnforcementMaxActive() != 2 || gen.EvidenceObjectID() != "op-rater" {
		t.Fatalf("gen=%+v", gen)
	}
	_ = runtimegen.StaticSource{Value: contrib}
}

func TestPhase53_BuildPublishesExecutableWhenRegistrationsPresent(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t, nil)
	opts.Production.RequestRegistrations = []authority.RequestRegistration{{
		Descriptor: phase53Req{id: "quota"}.Describe(),
		Priority:   authority.RequestPriorityQuotaBudgetRate,
		Provider:   phase53Req{id: "quota"},
	}}
	opts.Production.RaterRegistrations = []economics.RaterRegistration{{
		ID: "op-rater", Perspective: metering.PerspectiveOperator, Rater: phase53Rater{id: "op-rater"},
	}}
	_, built := mustProcessAndCandidate(t, cfg, opts)
	exec := runtimebundle.CandidateSnapshotGeneration(built).CurrentExecutable()
	if exec == nil {
		t.Fatal("expected executable generation after CompileCandidate")
	}
	if exec.EvidenceObjectID() != "op-rater" {
		t.Fatalf("evidence=%q", exec.EvidenceObjectID())
	}
}
