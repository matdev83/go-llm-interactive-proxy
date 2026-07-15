package configsource

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
)

// Source adapts validated config into an immutable authority rule snapshot.
type Source struct {
	snap app.RuleSnapshot
}

// New constructs a config-backed rule source from validated authority config.
func New(cfg config.AccountingAuthorityConfig) (*Source, error) {
	domainCfg, err := cfg.DomainConfig()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	version := strings.TrimSpace(cfg.SnapshotVersion)
	if version == "" {
		version = "static"
	}
	status := domainCfg.Status()
	snap := app.RuleSnapshot{
		ID:                 "usage_authority",
		Version:            version,
		EffectiveAt:        now,
		FetchedAt:          now,
		State:              app.SnapshotStateFromAuthority(status),
		Status:             status,
		UnknownAttribution: domainCfg.UnknownAttribution,
		Rules:              cloneRules(domainCfg.Rules),
	}
	return &Source{snap: snap}, nil
}

// Snapshot returns a deep copy of the stored config-backed rule snapshot.
func (s *Source) Snapshot(ctx context.Context) (app.RuleSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return app.RuleSnapshot{}, err
	}
	if s == nil {
		return app.RuleSnapshot{
			ID:     "usage_authority",
			State:  app.SnapshotStateFromAuthority(authoritydomain.StatusFromBacking(authoritydomain.BackingCapabilityDisabled)),
			Status: authoritydomain.StatusFromBacking(authoritydomain.BackingCapabilityDisabled),
		}, nil
	}
	out := s.snap
	out.Rules = cloneRules(s.snap.Rules)
	return out, nil
}

func cloneRules(in []authoritydomain.Rule) []authoritydomain.Rule {
	if len(in) == 0 {
		return nil
	}
	out := make([]authoritydomain.Rule, len(in))
	for i, rule := range in {
		out[i] = rule
		if len(rule.Match.Labels) > 0 {
			out[i].Match.Labels = make(map[string]authoritydomain.DimensionMatcher, len(rule.Match.Labels))
			maps.Copy(out[i].Match.Labels, rule.Match.Labels)
		}
	}
	return out
}

// Compile-time assertion that Source satisfies the app-owned RuleSource port.
var _ app.RuleSource = (*Source)(nil)
