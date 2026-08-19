package compactioncontinuity

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/policy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

func TestEffectiveConfigConsumesOnlyTrustedSessionPolicy(t *testing.T) {
	cfg, err := (Config{Extractor: ExtractorConfig{Enabled: true, Route: "route:default"}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	p := &Plugin{cfg: cfg}
	trusted := execctx.WithViews(context.Background(), execctx.Views{
		Scope:   scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1")},
		Session: session.SessionView{AuthoritativeSessionID: "session-1"},
	})
	override := policy.Override{Route: "route:approved", RouteSet: true, Limits: policy.LimitOverride{MaxInputTokens: intPtr(6000)}}
	got, ok := p.effectiveConfig(policy.WithTrustedOverride(trusted, override))
	if !ok || got.Extractor.Route != "route:approved" || got.Extractor.MaxInputTokens != 6000 {
		t.Fatalf("trusted policy was not consumed: ok=%v cfg=%+v", ok, got)
	}
	untrusted, ok := p.effectiveConfig(policy.WithTrustedOverride(context.Background(), override))
	if !ok || untrusted.Extractor.Route != "route:default" || untrusted.Extractor.MaxInputTokens == 6000 {
		t.Fatalf("untrusted policy changed feature config: ok=%v cfg=%+v", ok, untrusted)
	}
}

func TestEffectiveConfigTrustedDisableStopsContinuityWork(t *testing.T) {
	cfg, err := (Config{Extractor: ExtractorConfig{Enabled: true, Route: "route:default"}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	p := &Plugin{cfg: cfg}
	ctx := execctx.WithViews(context.Background(), execctx.Views{Session: session.SessionView{AuthoritativeSessionID: "session-1"}})
	ctx = policy.WithTrustedOverride(ctx, policy.Override{Enabled: boolPtr(false)})
	if _, ok := p.effectiveConfig(ctx); ok {
		t.Fatal("trusted session disable did not stop continuity work")
	}
}

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }
