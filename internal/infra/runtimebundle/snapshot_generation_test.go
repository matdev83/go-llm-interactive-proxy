package runtimebundle_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

func TestBuild_PublishesSnapshotGeneration(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	cfg.Accounting.Pricing.CatalogVersion = "prices-v3"
	cfg.Accounting.Pricing.Currency = "USD"
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), baseAuthorityOptions(t, nil))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() {
		for _, closer := range built.Closers {
			_ = closer()
		}
	})
	if built.SnapshotGeneration == nil || built.SnapshotGeneration.Current() == nil {
		t.Fatal("expected published snapshot generation")
	}
	cur := built.SnapshotGeneration.Current()
	if cur.Rating.Version != "prices-v3" || cur.State != economics.SnapshotReady {
		t.Fatalf("cur=%+v", cur)
	}
	held := cur
	built.SnapshotGeneration.MarkUnusable(economics.SnapshotDegraded, "refresh_failed")
	if held.Rating.Version != "prices-v3" {
		t.Fatalf("in-flight generation mutated: %+v", held)
	}
	if built.SnapshotGeneration.Current().State != economics.SnapshotDegraded {
		t.Fatalf("expected degraded current")
	}
	if built.Executor == nil || built.Executor.SnapshotGeneration == nil {
		t.Fatal("executor must receive SnapshotGeneration for admit-time binding")
	}
	if built.Executor.SnapshotGeneration != built.SnapshotGeneration {
		t.Fatal("executor SnapshotGeneration must be the same publisher instance")
	}
}
