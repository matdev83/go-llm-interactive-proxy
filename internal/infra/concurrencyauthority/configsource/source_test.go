package configsource_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/concurrencyauthority/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestSourceSnapshot(t *testing.T) {
	t.Parallel()
	src, err := configsource.New(config.ConcurrencyAuthorityConfig{
		Enabled: true,
		Rules: []config.ConcurrencyAuthorityRuleConfig{{
			ID:                "max-active",
			Mode:              "strict",
			MaxActiveRequests: 5,
			Match: config.AccountingAuthorityDimensionsConfig{
				Principal: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("p1")},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := src.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Rules) != 1 || snap.Rules[0].Limit != 5 {
		t.Fatalf("snap=%+v", snap)
	}
}
