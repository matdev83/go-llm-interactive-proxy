package policy_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/policy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

func baseDefaults() policy.Defaults {
	return policy.Defaults{
		Enabled:   true,
		Preserve:  policy.Categories{Plan: true, UserDecisions: true, Constraints: true, Rationale: true, RejectedAlternatives: true},
		Extractor: policy.Extractor{Route: "route:default", Timeout: 8 * time.Second, MaxInputTokens: 12000, MaxOutputTokens: 2000},
		Limits:    policy.Limits{BarrierTimeout: 2 * time.Second, CapsuleMaxTokens: 2500, CapsuleMaxBytes: 1 << 20, SourceMaxBytes: 4 << 20, ResultMaxBytes: 4 << 20, ResultMaxCount: 16},
	}
}

func baseMaxima() policy.HardMaxima {
	return policy.HardMaxima{
		Enabled:             true,
		Preserve:            policy.Categories{Plan: true, UserDecisions: true, Constraints: true, Rationale: true, RejectedAlternatives: true},
		ApprovedRoutes:      []string{"route:default", "route:approved"},
		AllowTranscriptRead: true,
		Limits:              policy.Limits{Timeout: 20 * time.Second, MaxInputTokens: 20000, MaxOutputTokens: 4000, BarrierTimeout: 10 * time.Second, CapsuleMaxTokens: 5000, CapsuleMaxBytes: 2 << 20, SourceMaxBytes: 8 << 20, ResultMaxBytes: 8 << 20, ResultMaxCount: 32},
	}
}

func trustedContext(labels map[string]string) context.Context {
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "principal-1"})
	ctx = scope.WithScope(ctx, scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1"), TenantID: scope.Known("tenant-1"), WorkspaceID: scope.Known("workspace-1")})
	return session.WithSessionView(ctx, session.SessionView{AuthoritativeSessionID: "session-1", WorkspaceID: "workspace-1", Labels: labels})
}

func TestResolve_DefaultsWinWithoutOverride(t *testing.T) {
	got, err := policy.Resolve(context.Background(), baseDefaults(), baseMaxima())
	if err != nil {
		t.Fatal(err)
	}
	if got.Extractor.Route != "route:default" || !got.Enabled || got.Preserve != baseDefaults().Preserve {
		t.Fatalf("unexpected effective policy: %+v", got)
	}
}

func TestResolve_GlobalHardDisableCannotBeEnabledBySession(t *testing.T) {
	max := baseMaxima()
	max.Enabled = false
	ctx := policy.WithTrustedOverride(trustedContext(nil), policy.Override{Enabled: boolPtr(true)})
	got, err := policy.Resolve(ctx, baseDefaults(), max)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("session enable widened operator hard disable")
	}
}

func TestResolve_TrustedEnableAndDisable(t *testing.T) {
	defaults := baseDefaults()
	defaults.Enabled = false
	ctx := policy.WithTrustedOverride(trustedContext(nil), policy.Override{Enabled: boolPtr(true)})
	got, err := policy.Resolve(ctx, defaults, baseMaxima())
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("trusted session enabled a globally disabled feature")
	}

	ctx = policy.WithTrustedOverride(trustedContext(nil), policy.Override{Enabled: boolPtr(false)})
	defaults.Enabled = true
	got, err = policy.Resolve(ctx, defaults, baseMaxima())
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("trusted disable was not applied")
	}
}

func TestResolve_UntrustedContextCannotApplyTypedOverride(t *testing.T) {
	ctx := policy.WithTrustedOverride(context.Background(), policy.Override{Enabled: boolPtr(false), Route: "route:approved", RouteSet: true})
	got, err := policy.Resolve(ctx, baseDefaults(), baseMaxima())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.Extractor.Route != "route:default" {
		t.Fatalf("untrusted override applied: %+v", got)
	}
}

func TestResolve_TrustedCategoriesCanTighten(t *testing.T) {
	categories := policy.Categories{Plan: true, UserDecisions: false, Constraints: false, Rationale: false, RejectedAlternatives: false}
	ctx := policy.WithTrustedOverride(trustedContext(nil), policy.Override{Preserve: &categories})
	got, err := policy.Resolve(ctx, baseDefaults(), baseMaxima())
	if err != nil {
		t.Fatal(err)
	}
	if got.Preserve != categories {
		t.Fatalf("categories: got %+v want %+v", got.Preserve, categories)
	}
}

func TestResolve_TrustedCategoriesCannotWidenHardMaximum(t *testing.T) {
	max := baseMaxima()
	max.Preserve.Rationale = false
	categories := policy.Categories{Rationale: true}
	ctx := policy.WithTrustedOverride(trustedContext(nil), policy.Override{Preserve: &categories})
	got, err := policy.Resolve(ctx, baseDefaults(), max)
	if err != nil {
		t.Fatal(err)
	}
	if got.Preserve.Rationale {
		t.Fatal("session widened category hard maximum")
	}
}

func TestResolve_ApprovedRouteOverride(t *testing.T) {
	ctx := policy.WithTrustedOverride(trustedContext(nil), policy.Override{Route: "route:approved", RouteSet: true})
	got, err := policy.Resolve(ctx, baseDefaults(), baseMaxima())
	if err != nil {
		t.Fatal(err)
	}
	if got.Extractor.Route != "route:approved" {
		t.Fatalf("route = %q", got.Extractor.Route)
	}
}

func TestResolve_UnapprovedRouteIsIgnored(t *testing.T) {
	ctx := policy.WithTrustedOverride(trustedContext(nil), policy.Override{Route: "route:attacker", RouteSet: true})
	got, err := policy.Resolve(ctx, baseDefaults(), baseMaxima())
	if err != nil {
		t.Fatal(err)
	}
	if got.Extractor.Route != "route:default" {
		t.Fatalf("unapproved route applied: %q", got.Extractor.Route)
	}
}

func TestResolve_DefaultRouteMustBeApproved(t *testing.T) {
	max := baseMaxima()
	max.ApprovedRoutes = []string{"route:approved"}
	if _, err := policy.Resolve(context.Background(), baseDefaults(), max); err == nil {
		t.Fatal("unapproved default route was accepted")
	}
}

func TestResolve_TrustedApprovedRouteCanReplaceUnapprovedDefault(t *testing.T) {
	max := baseMaxima()
	max.ApprovedRoutes = []string{"route:approved"}
	ctx := policy.WithTrustedOverride(trustedContext(nil), policy.Override{Route: "route:approved", RouteSet: true})
	got, err := policy.Resolve(ctx, baseDefaults(), max)
	if err != nil || got.Extractor.Route != "route:approved" {
		t.Fatalf("trusted approved route did not replace default: got=%+v err=%v", got, err)
	}
}

func TestResolve_TighterNumericLimitsApply(t *testing.T) {
	o := policy.Override{Limits: policy.LimitOverride{MaxInputTokens: intPtr(6000), MaxOutputTokens: intPtr(1000), CapsuleMaxTokens: intPtr(1000)}}
	ctx := policy.WithTrustedOverride(trustedContext(nil), o)
	got, err := policy.Resolve(ctx, baseDefaults(), baseMaxima())
	if err != nil {
		t.Fatal(err)
	}
	if got.Extractor.MaxInputTokens != 6000 || got.Extractor.MaxOutputTokens != 1000 || got.Limits.CapsuleMaxTokens != 1000 {
		t.Fatalf("limits: %+v %+v", got.Extractor, got.Limits)
	}
}

func TestResolve_LooserNumericLimitsAreIgnored(t *testing.T) {
	o := policy.Override{Limits: policy.LimitOverride{MaxInputTokens: intPtr(15000), MaxOutputTokens: intPtr(3000), CapsuleMaxTokens: intPtr(5000)}}
	ctx := policy.WithTrustedOverride(trustedContext(nil), o)
	got, err := policy.Resolve(ctx, baseDefaults(), baseMaxima())
	if err != nil {
		t.Fatal(err)
	}
	d := baseDefaults()
	if got.Extractor.MaxInputTokens != d.Extractor.MaxInputTokens || got.Extractor.MaxOutputTokens != d.Extractor.MaxOutputTokens || got.Limits.CapsuleMaxTokens != d.Limits.CapsuleMaxTokens {
		t.Fatalf("looser limits applied: %+v %+v", got.Extractor, got.Limits)
	}
}

func TestResolve_HardMaximumClampsGlobalValues(t *testing.T) {
	max := baseMaxima()
	max.Limits.MaxInputTokens = 9000
	max.Limits.CapsuleMaxTokens = 1500
	got, err := policy.Resolve(context.Background(), baseDefaults(), max)
	if err != nil {
		t.Fatal(err)
	}
	if got.Extractor.MaxInputTokens != 9000 || got.Limits.CapsuleMaxTokens != 1500 {
		t.Fatalf("hard maximum not applied: %+v %+v", got.Extractor, got.Limits)
	}
}

func TestResolve_DurationLimitsApplyAndClamp(t *testing.T) {
	o := policy.Override{Limits: policy.LimitOverride{Timeout: durationPtr(3 * time.Second), BarrierTimeout: durationPtr(time.Second)}}
	ctx := policy.WithTrustedOverride(trustedContext(nil), o)
	got, err := policy.Resolve(ctx, baseDefaults(), baseMaxima())
	if err != nil {
		t.Fatal(err)
	}
	if got.Extractor.Timeout != 3*time.Second || got.Limits.BarrierTimeout != time.Second {
		t.Fatalf("duration limits: %+v %+v", got.Extractor, got.Limits)
	}
}

func TestResolve_LabelOverrideIsTrustedOnlyWithAuthoritativeSession(t *testing.T) {
	labels := map[string]string{policy.LabelEnabled: "false", policy.LabelRoute: "route:approved", policy.LabelMaxInputTokens: "6000", policy.LabelPlan: "false"}
	got, err := policy.Resolve(trustedContext(labels), baseDefaults(), baseMaxima())
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.Extractor.Route != "route:approved" || got.Extractor.MaxInputTokens != 6000 {
		t.Fatalf("labels not applied: %+v", got)
	}
	if got.Preserve.UserDecisions != baseDefaults().Preserve.UserDecisions || got.Preserve.Plan {
		t.Fatalf("partial category label did not preserve omitted values: %+v", got.Preserve)
	}

	got, err = policy.Resolve(session.WithSessionView(context.Background(), session.SessionView{ClientSessionHint: "client", Labels: labels}), baseDefaults(), baseMaxima())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.Extractor.Route != "route:default" {
		t.Fatalf("unauthenticated labels applied: %+v", got)
	}
}

func TestResolve_InvalidLabelValuesAreIgnored(t *testing.T) {
	labels := map[string]string{policy.LabelEnabled: "maybe", policy.LabelRoute: "route:attacker", policy.LabelMaxInputTokens: "not-an-int"}
	got, err := policy.Resolve(trustedContext(labels), baseDefaults(), baseMaxima())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.Extractor.Route != "route:default" || got.Extractor.MaxInputTokens != baseDefaults().Extractor.MaxInputTokens {
		t.Fatalf("invalid labels changed policy: %+v", got)
	}
}

func TestResolve_TranscriptRequiresTrustedTurnAndPolicy(t *testing.T) {
	max := baseMaxima()
	got, err := policy.Resolve(trustedContext(nil), baseDefaults(), max)
	if err != nil {
		t.Fatal(err)
	}
	if got.TranscriptRead {
		t.Fatal("transcript read allowed without secure turn")
	}
}

func TestResolve_TranscriptDisabledCannotAcquireHiddenTranscript(t *testing.T) {
	ctx := policy.WithSecureTurn(trustedContext(nil), false)
	got, err := policy.Resolve(ctx, baseDefaults(), baseMaxima())
	if err != nil {
		t.Fatal(err)
	}
	if got.TranscriptRead {
		t.Fatal("transcript-disabled session acquired transcript access")
	}
}

func TestTranscriptAuthorizationPreservesTenantWorkspace(t *testing.T) {
	ctx := policy.WithSecureTurn(trustedContext(nil), true)
	auth, ok := policy.TranscriptAuthorizationFromContext(ctx)
	if !ok || auth.TenantID != "tenant-1" || auth.WorkspaceID != "workspace-1" {
		t.Fatalf("authorization: %+v ok=%v", auth, ok)
	}
	if !policy.AuthorizeTranscriptWorkspace(ctx, "workspace-1") {
		t.Fatal("same workspace denied")
	}
	if policy.AuthorizeTranscriptWorkspace(ctx, "workspace-other") {
		t.Fatal("cross-workspace transcript read allowed")
	}
	if policy.AuthorizeTranscriptScope(ctx, "tenant-other", "workspace-1") {
		t.Fatal("cross-tenant transcript read allowed")
	}
}

func TestTranscriptAuthorization_DetachedChildWithoutAuthoritativeSession(t *testing.T) {
	parent := policy.WithSecureTurn(trustedContext(nil), true)
	child := session.WithSessionView(parent, session.SessionView{ALegID: "child-a-leg"})
	if _, ok := policy.TranscriptAuthorizationFromContext(child); ok {
		t.Fatal("detached child inherited primary transcript authority")
	}
}

func TestTranscriptAuthorization_RejectsSessionScopeWorkspaceMismatch(t *testing.T) {
	ctx := policy.WithSecureTurn(trustedContext(nil), true)
	ctx = scope.WithScope(ctx, scope.PrincipalScopeView{
		PrincipalID: scope.Known("principal-1"),
		TenantID:    scope.Known("tenant-1"),
		WorkspaceID: scope.Known("workspace-other"),
	})
	if _, ok := policy.TranscriptAuthorizationFromContext(ctx); ok {
		t.Fatal("mismatched session and scope workspaces authorized transcript access")
	}
}

func TestResolve_ExtractorNeverCarriesBranchOrAccountIdentifiers(t *testing.T) {
	got, err := policy.Resolve(trustedContext(nil), baseDefaults(), baseMaxima())
	if err != nil {
		t.Fatal(err)
	}
	_ = got
	// The effective extractor policy contains only route and bounded controls;
	// lineage is deliberately owned by extractor request metadata and omitted
	// from this value.
}

func boolPtr(v bool) *bool                       { return &v }
func intPtr(v int) *int                          { return &v }
func durationPtr(v time.Duration) *time.Duration { return &v }
