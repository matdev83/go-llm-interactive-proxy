package lipruntime_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
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
	querier := &recordingQuerier{}
	rt, err := lipruntime.Build(ctx, lipruntime.Options{
		ConfigPath:       repoConfigPath(t),
		MeteringRecorder: meter,
		MeteringQuerier:  querier,
		EvidenceSink:     sink,
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
	caps := rt.Capabilities()
	if !caps.ProductionMetering {
		t.Fatal("production metering recorder must be wired outside TestingOptions")
	}
	if !caps.ProductionEvidenceSink {
		t.Fatal("production evidence sink must be accepted and retained")
	}
	if !caps.ProductionMeteringQuerier || rt.MeteringQuerier() == nil {
		t.Fatal("production metering querier must be mounted")
	}
	if caps.SnapshotGenerationID == 0 {
		t.Fatal("expected published snapshot generation")
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

func TestOptions_DoesNotExposeBillingStore(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[lipruntime.Options]()
	for _, name := range []string{
		"BillingStore",
		"BillingJournal",
		"BillingCatalog",
		"BillingAccount",
		"BillingRatingResolver",
		"AccountProvisioner",
		"ComposeBilling",
	} {
		if _, ok := typ.FieldByName(name); ok {
			t.Fatalf("public Options must not invent a %s field", name)
		}
	}
}

func TestBuild_AuthoritativeBillingYAMLFailsClosedWithoutInjection(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(repoConfigPath(t))
	if err != nil {
		t.Fatal(err)
	}
	patched := append(append([]byte{}, raw...), []byte("\naccounting:\n  billing:\n    authoritative: true\n")...)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, patched, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = lipruntime.Build(context.Background(), lipruntime.Options{ConfigPath: path})
	if err == nil {
		t.Fatal("expected fail-closed authoritative billing without injected store")
	}
	if !strings.Contains(err.Error(), "authoritative billing") {
		t.Fatalf("error = %v, want authoritative billing fail-closed", err)
	}
}
