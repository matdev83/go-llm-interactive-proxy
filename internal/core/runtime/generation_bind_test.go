package runtime

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

func TestApplyGenerationBoundVersion_PrefersPublishedGeneration(t *testing.T) {
	t.Parallel()
	pub := snapshotgen.NewPublisher()
	pub.Publish(snapshotgen.RuntimeGeneration{
		State: economics.SnapshotReady,
		Usage: economics.Snapshot[economics.PolicyRulesView]{
			ID: "usage_authority", Version: "gen-v9", State: economics.SnapshotReady,
			EffectiveAt: time.Unix(1, 0).UTC(),
			Value:       economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
		},
	})
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{SnapshotGeneration: pub},
	}
	res := authorityapp.AdmissionResult{
		BoundVersion: economics.PolicySnapshotRef{
			VersionRef: economics.VersionRef{ID: "usage_authority", Version: "rules-old"},
			PolicyID:   "usage_authority",
		},
	}
	ex.applyGenerationBoundVersion(&res)
	if res.BoundVersion.Version != "gen-v9" {
		t.Fatalf("bound=%+v, want gen-v9 from SnapshotGeneration", res.BoundVersion)
	}
}
