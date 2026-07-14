package runtimebundle

import (
	"context"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/snapshotsource"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

func buildSnapshotGeneration(cfg *config.Config, testing TestingOptions, prod ProductionOptions) *snapshotgen.Publisher {
	if testing.SnapshotPublisherOverride != nil {
		return testing.SnapshotPublisherOverride
	}
	pub := snapshotgen.NewPublisher()
	now := time.Now().UTC()
	if testing.Clock != nil {
		now = testing.Clock().UTC()
	}
	gen := snapshotgen.RuntimeGeneration{
		PublishedAt: now,
		State:       economics.SnapshotReady,
	}
	ctx := context.Background()
	if cfg != nil {
		usageVer := "static"
		if v := cfg.Accounting.Authority.SnapshotVersion; v != "" {
			usageVer = v
		}
		if cfg.Accounting.Authority.Enabled {
			gen.Usage = economics.Snapshot[economics.PolicyRulesView]{
				ID: "usage_authority", Version: usageVer, EffectiveAt: now, FetchedAt: now,
				State: economics.SnapshotReady,
				Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
			}
		} else {
			gen.Usage = economics.Snapshot[economics.PolicyRulesView]{
				ID: "usage_authority", Version: usageVer, State: economics.SnapshotDisabled,
				Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
			}
		}
		concVer := "static"
		if v := cfg.Accounting.Concurrency.SnapshotVersion; v != "" {
			concVer = v
		}
		if cfg.Accounting.Concurrency.Enabled {
			gen.Concurrency = economics.Snapshot[economics.PolicyRulesView]{
				ID: "concurrency", Version: concVer, EffectiveAt: now, FetchedAt: now,
				State: economics.SnapshotReady,
				Value: economics.PolicyRulesView{Kind: economics.PolicyKindConcurrency},
			}
		} else {
			gen.Concurrency = economics.Snapshot[economics.PolicyRulesView]{
				ID: "concurrency", Version: concVer, State: economics.SnapshotDisabled,
				Value: economics.PolicyRulesView{Kind: economics.PolicyKindConcurrency},
			}
		}
		catVer := cfg.Accounting.Pricing.CatalogVersion
		gen.Rating = snapshotsource.StaticRatingFromCatalog("rating", catVer, cfg.Accounting.Pricing.Currency, now)
	}
	if prod.UsageSnapshotSource != nil {
		if snap, err := prod.UsageSnapshotSource.Snapshot(ctx); err == nil {
			gen.Usage = snap
		}
	}
	if prod.ConcurrencySnapshotSource != nil {
		if snap, err := prod.ConcurrencySnapshotSource.Snapshot(ctx); err == nil {
			gen.Concurrency = snap
		}
	}
	if prod.RatingSnapshotSource != nil {
		if snap, err := prod.RatingSnapshotSource.Snapshot(ctx); err == nil {
			gen.Rating = snap
		}
	}
	pub.Publish(gen)
	return pub
}
