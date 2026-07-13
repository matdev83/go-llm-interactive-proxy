package configsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestSourceSnapshotCapturesValidatedConfigSnapshot(t *testing.T) {
	t.Parallel()

	anchor := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	cfg := config.AccountingAuthorityConfig{
		Enabled:        true,
		Mode:           "strict",
		Store:          "memory",
		StartupPosture: "fail_closed",
		Rules: []config.AccountingAuthorityRuleConfig{
			{
				ID:       "tenant.requests",
				Kind:     "quota",
				Mode:     "strict",
				Unit:     "requests",
				Limit:    10,
				Currency: "usd",
				Window: config.AccountingAuthorityWindowConfig{
					Algorithm: "fixed",
					Size:      "1h",
					Anchor:    anchor.Format(time.RFC3339),
				},
				Match: config.AccountingAuthorityDimensionsConfig{
					Principal: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Unknown()},
					Tenant:    config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("")},
					Backend:   config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("backend-1")},
					Labels: map[string]config.AccountingAuthorityDimensionMatcherConfig{
						"team": {Value: scope.Known("platform")},
					},
				},
			},
		},
	}

	src, err := configsource.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	snap, err := src.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if snap.Status.State != authoritydomain.AuthorityStateReady {
		t.Fatalf("status.state = %q, want %q", snap.Status.State, authoritydomain.AuthorityStateReady)
	}
	if snap.Status.Reason != authoritydomain.StatusReasonNone {
		t.Fatalf("status.reason = %q, want %q", snap.Status.Reason, authoritydomain.StatusReasonNone)
	}
	if snap.UnknownAttribution != authoritydomain.UnknownAttributionPreserve {
		t.Fatalf("unknown attribution = %q, want preserve", snap.UnknownAttribution)
	}
	if len(snap.Rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(snap.Rules))
	}

	rule := snap.Rules[0]
	if rule.ID != "tenant.requests" {
		t.Fatalf("rule id = %q, want %q", rule.ID, "tenant.requests")
	}
	if rule.Kind != authoritydomain.RuleKindQuota {
		t.Fatalf("rule kind = %q, want %q", rule.Kind, authoritydomain.RuleKindQuota)
	}
	if rule.Mode != authoritydomain.RuleModeStrict {
		t.Fatalf("rule mode = %q, want %q", rule.Mode, authoritydomain.RuleModeStrict)
	}
	if rule.Unit != authoritydomain.AmountUnitRequests {
		t.Fatalf("rule unit = %q, want %q", rule.Unit, authoritydomain.AmountUnitRequests)
	}
	if rule.Limit.Unit != authoritydomain.AmountUnitRequests || rule.Limit.Value != 10 {
		t.Fatalf("rule limit = %#v, want 10 requests", rule.Limit)
	}
	if rule.Limit.Currency != "usd" || rule.Currency != "usd" {
		t.Fatalf("rule currency = %q/%q, want usd", rule.Limit.Currency, rule.Currency)
	}
	if rule.Window.Algorithm != authoritydomain.WindowAlgorithmFixed {
		t.Fatalf("window algorithm = %q, want %q", rule.Window.Algorithm, authoritydomain.WindowAlgorithmFixed)
	}
	if rule.Window.Size != time.Hour {
		t.Fatalf("window size = %s, want %s", rule.Window.Size, time.Hour)
	}
	if !rule.Window.Anchor.Equal(anchor.UTC()) {
		t.Fatalf("window anchor = %s, want %s", rule.Window.Anchor, anchor.UTC())
	}
	bounds, err := rule.Window.Bounds(anchor.Add(30 * time.Minute))
	if err != nil {
		t.Fatalf("Window.Bounds: %v", err)
	}
	if !bounds.Start.Equal(anchor.UTC()) || !bounds.End.Equal(anchor.Add(time.Hour)) {
		t.Fatalf("window bounds = %s..%s, want %s..%s", bounds.Start, bounds.End, anchor.UTC(), anchor.Add(time.Hour))
	}
	if !rule.Match.Backend.Value.Equal(scope.Known("backend-1")) {
		t.Fatalf("backend matcher = %#v, want backend-1", rule.Match.Backend.Value)
	}
	if !rule.Match.Principal.Value.IsUnknown() {
		t.Fatalf("principal matcher = %#v, want unknown", rule.Match.Principal.Value)
	}
	if !rule.Match.Tenant.Value.IsKnownEmpty() {
		t.Fatalf("tenant matcher = %#v, want known-empty", rule.Match.Tenant.Value)
	}
	if got := rule.Match.Labels["team"].Value; !got.Equal(scope.Known("platform")) {
		t.Fatalf("label matcher = %#v, want platform", got)
	}
}

func TestSourceSnapshotCarriesUnknownAttributionMode(t *testing.T) {
	t.Parallel()

	src, err := configsource.New(config.AccountingAuthorityConfig{
		Enabled:            true,
		Mode:               "strict",
		Store:              "memory",
		StartupPosture:     "fail_closed",
		UnknownAttribution: "known_empty",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	snap, err := src.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.UnknownAttribution != authoritydomain.UnknownAttributionKnownEmpty {
		t.Fatalf("unknown attribution = %q, want known_empty", snap.UnknownAttribution)
	}
}

func TestSourceSnapshotMapsFailurePosture(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		posture    string
		wantState  authoritydomain.AuthorityState
		wantReason authoritydomain.StatusReason
	}{
		{
			name:       "fail_closed",
			posture:    "fail_closed",
			wantState:  authoritydomain.AuthorityStateReady,
			wantReason: authoritydomain.StatusReasonNone,
		},
		{
			name:       "fail_open",
			posture:    "fail_open",
			wantState:  authoritydomain.AuthorityStateAdvisoryOnly,
			wantReason: authoritydomain.StatusReasonAdvisoryOnly,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src, err := configsource.New(config.AccountingAuthorityConfig{
				Enabled:        true,
				Mode:           "strict",
				Store:          "memory",
				StartupPosture: tt.posture,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			snap, err := src.Snapshot(context.Background())
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			if snap.Status.State != tt.wantState {
				t.Fatalf("status.state = %q, want %q", snap.Status.State, tt.wantState)
			}
			if snap.Status.Reason != tt.wantReason {
				t.Fatalf("status.reason = %q, want %q", snap.Status.Reason, tt.wantReason)
			}
		})
	}
}

func TestSourceSnapshotIsDeepCopiedFromConfigAndCallers(t *testing.T) {
	t.Parallel()

	cfg := config.AccountingAuthorityConfig{
		Enabled:        true,
		Mode:           "strict",
		Store:          "memory",
		StartupPosture: "fail_closed",
		Rules: []config.AccountingAuthorityRuleConfig{
			{
				ID:    "tenant.requests",
				Kind:  "quota",
				Mode:  "strict",
				Unit:  "requests",
				Limit: 10,
				Window: config.AccountingAuthorityWindowConfig{
					Algorithm: "fixed",
					Size:      "1h",
					Anchor:    time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
				},
				Match: config.AccountingAuthorityDimensionsConfig{
					Backend: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("backend-1")},
					Labels: map[string]config.AccountingAuthorityDimensionMatcherConfig{
						"team": {Value: scope.Known("platform")},
					},
				},
			},
		},
	}

	src, err := configsource.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	snap, err := src.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	cfg.Rules[0].ID = "mutated"
	cfg.Rules[0].Window.Size = "2h"
	cfg.Rules[0].Match.Labels["team"] = config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("changed")}
	snap.Rules[0].ID = "tampered"
	snap.Rules[0].Match.Labels["team"] = authoritydomain.DimensionMatcher{Value: scope.Known("rewritten")}

	snap2, err := src.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot after mutation: %v", err)
	}
	if snap2.Rules[0].ID != "tenant.requests" {
		t.Fatalf("snapshot rule id = %q, want %q", snap2.Rules[0].ID, "tenant.requests")
	}
	if snap2.Rules[0].Window.Size != time.Hour {
		t.Fatalf("snapshot window size = %s, want %s", snap2.Rules[0].Window.Size, time.Hour)
	}
	if got := snap2.Rules[0].Match.Labels["team"].Value; !got.Equal(scope.Known("platform")) {
		t.Fatalf("snapshot label = %#v, want platform", got)
	}
}
