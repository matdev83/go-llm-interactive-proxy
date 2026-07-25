package runtimebundle_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

func TestBuild_WiresReadinessReportWithoutExecutorExport(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t, nil)
	_, built := mustProcessAndCandidate(t, cfg, opts)
	if runtimebundle.CandidateReadinessReport(built) == nil {
		t.Fatal("expected readiness report service on CandidateRuntime")
	}
	report, err := runtimebundle.CandidateReadinessReport(built).Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Components) == 0 {
		t.Fatal("expected independent readiness components")
	}
	if !report.Posture.LastUpdatedAt.IsZero() && report.Posture.State == "" {
		t.Fatalf("posture=%#v", report.Posture)
	}
	for _, c := range report.Components {
		if c.Component == controlplane.ReadinessComponentMeteringJournal &&
			c.EnforcementScope == controlplane.EnforcementScopeDistributedStrict {
			t.Fatalf("memory metering must not report distributed strict: %#v", c)
		}
	}
}
