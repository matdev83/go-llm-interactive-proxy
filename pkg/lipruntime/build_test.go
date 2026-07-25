package lipruntime_test

import (
	"context"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

func repoConfigPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "config", "config.yaml")
}

type recordingMeter struct {
	n atomic.Int64
}

func (r *recordingMeter) Append(context.Context, metering.Fact) error {
	r.n.Add(1)
	return nil
}

type staticRuleSource struct {
	snap economics.Snapshot[economics.PolicyRulesView]
}

func (s staticRuleSource) Snapshot(context.Context) (economics.Snapshot[economics.PolicyRulesView], error) {
	return s.snap, nil
}

type staticRatingSource struct {
	snap economics.Snapshot[economics.RatingCatalogView]
}

func (s staticRatingSource) Snapshot(context.Context) (economics.Snapshot[economics.RatingCatalogView], error) {
	return s.snap, nil
}

type allowRequestProvider struct{}

func (allowRequestProvider) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow, ProviderID: "enterprise-req"}, nil
}

func (allowRequestProvider) SettleRequest(_ context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (allowRequestProvider) ReleaseRequest(context.Context, authority.RequestRelease) error {
	return nil
}

func TestBuild_PublicOnlyOptions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	meter := &recordingMeter{}
	now := time.Unix(200, 0).UTC()
	sink := &recordingEvidence{}
	rater := &recordingRater{}
	querier := &recordingQuerier{}
	rt, err := lipruntime.Build(ctx, lipruntime.Options{
		ConfigPath:       repoConfigPath(t),
		MeteringRecorder: meter,
		MeteringQuerier:  querier,
		EvidenceSink:     sink,
		RaterRegistrations: []economics.RaterRegistration{{
			ID: "enterprise-operator-rater", Perspective: metering.PerspectiveOperator, Rater: rater,
		}},
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "enterprise-req",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageRequestAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: allowRequestProvider{},
		}},
		UsageSnapshotSource: staticRuleSource{snap: economics.Snapshot[economics.PolicyRulesView]{
			ID: "usage_authority", Version: "ent-v1", EffectiveAt: now, FetchedAt: now,
			State: economics.SnapshotReady,
			Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
		}},
		RatingSnapshotSource: staticRatingSource{snap: economics.Snapshot[economics.RatingCatalogView]{
			ID: "rating", Version: "ent-prices", State: economics.SnapshotReady,
			Value: economics.RatingCatalogView{CatalogVersion: "ent-prices", Currency: "USD"},
		}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if !rt.Ready() {
		t.Fatal("expected ready runtime")
	}
	if rt.ExecutorView() == nil {
		t.Fatal("expected ExecutorView")
	}
	if st := rt.ReloadStatus(); st.ActiveGeneration < 1 {
		t.Fatalf("active generation=%d want >= 1", st.ActiveGeneration)
	}
}

type recordingEvidence struct{}

func (recordingEvidence) RecordPolicyDecision(context.Context, policydecision.Record) error {
	return nil
}

func (recordingEvidence) RecordAccountingAuthority(context.Context, controlplane.Event) error {
	return nil
}

type recordingRater struct{}

func (recordingRater) Rate(_ context.Context, req economics.RatingRequest) (economics.RatingResult, error) {
	return economics.RatingResult{
		Money:       economics.Money{Present: true, NanoUnits: 1, Currency: "USD"},
		Perspective: req.Perspective,
		RaterID:     "recording",
		Version:     economics.VersionRef{ID: "recording", Version: "v1"},
	}, nil
}

type recordingQuerier struct{}

func (recordingQuerier) List(context.Context, metering.Query) (metering.Page, error) {
	return metering.Page{}, nil
}

func TestBuild_RejectsEmptyConfigPath(t *testing.T) {
	t.Parallel()
	_, err := lipruntime.Build(context.Background(), lipruntime.Options{})
	if err == nil {
		t.Fatal("expected error")
	}
}
