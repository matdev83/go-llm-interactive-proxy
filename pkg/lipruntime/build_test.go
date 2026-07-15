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
func (allowRequestProvider) SettleRequest(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
	return authority.Settlement{Kind: authority.SettlementFinal}, nil
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
		Rater:            rater,
		RequestProviders: []authority.RequestProvider{allowRequestProvider{}},
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
	if !rt.HasProductionMetering() {
		t.Fatal("production metering recorder must be wired outside TestingOptions")
	}
	if !rt.HasProductionEvidenceSink() {
		t.Fatal("production evidence sink must be accepted and retained")
	}
	if !rt.HasProductionRater() {
		t.Fatal("production rater must be forwarded onto the accounting runtime")
	}
	if !rt.HasProductionMeteringQuerier() {
		t.Fatal("production metering querier must be mounted")
	}
	if rt.SnapshotGenerationID() == 0 {
		t.Fatal("expected published snapshot generation")
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
	return economics.RatingResult{Money: economics.Money{Present: true, NanoUnits: 1, Currency: "USD"}, Perspective: req.Perspective}, nil
}

type recordingQuerier struct{}

func (recordingQuerier) List(context.Context, metering.Query) (metering.Page, error) {
	return metering.Page{}, nil
}

type mutableRuleSource struct {
	ver atomic.Value // string
	err atomic.Value // error
}

func (m *mutableRuleSource) Snapshot(context.Context) (economics.Snapshot[economics.PolicyRulesView], error) {
	if e, ok := m.err.Load().(error); ok && e != nil {
		return economics.Snapshot[economics.PolicyRulesView]{}, e
	}
	ver, _ := m.ver.Load().(string)
	now := time.Unix(300, 0).UTC()
	return economics.Snapshot[economics.PolicyRulesView]{
		ID: "usage_authority", Version: ver, EffectiveAt: now, FetchedAt: now,
		State: economics.SnapshotReady,
		Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
	}, nil
}

// Requirement 11.3/11.6: public RefreshSnapshots republishes newer injected source versions.
func TestRuntime_RefreshSnapshotsPublishesNewerSource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := &mutableRuleSource{}
	src.ver.Store("pub-v1")
	rt, err := lipruntime.Build(ctx, lipruntime.Options{
		ConfigPath:          repoConfigPath(t),
		UsageSnapshotSource: src,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	beforeID := rt.SnapshotGenerationID()
	if beforeID == 0 {
		t.Fatal("expected initial generation")
	}
	if got := rt.SnapshotUsageVersion(); got != "pub-v1" {
		t.Fatalf("usage version=%q want pub-v1", got)
	}
	src.ver.Store("pub-v2")
	if err := rt.RefreshSnapshots(ctx); err != nil {
		t.Fatalf("RefreshSnapshots: %v", err)
	}
	afterID := rt.SnapshotGenerationID()
	if afterID <= beforeID {
		t.Fatalf("refresh must publish new generation; before=%d after=%d", beforeID, afterID)
	}
	if got := rt.SnapshotUsageVersion(); got != "pub-v2" {
		t.Fatalf("usage version=%q want pub-v2", got)
	}
}

func TestBuild_RejectsEmptyConfigPath(t *testing.T) {
	t.Parallel()
	_, err := lipruntime.Build(context.Background(), lipruntime.Options{})
	if err == nil {
		t.Fatal("expected error")
	}
}
