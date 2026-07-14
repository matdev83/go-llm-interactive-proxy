package runtimebundle_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

func TestBuild_WiresReadinessReportWithoutExecutorExport(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t, nil)
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() {
		for _, c := range built.Closers {
			_ = c()
		}
	})
	if built.ReadinessReport == nil {
		t.Fatal("expected readiness report service on Built")
	}
	report, err := built.ReadinessReport.Report(context.Background())
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
