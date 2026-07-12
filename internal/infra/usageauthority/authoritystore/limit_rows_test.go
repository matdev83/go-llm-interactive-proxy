package authoritystore_test

import (
	"context"
	"testing"
	"time"

	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestLimitRowsFromRulesDerivesLiveRows(t *testing.T) {
	t.Parallel()

	anchor := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	at := anchor.Add(30 * time.Minute)
	rules := []authoritydomain.Rule{
		{
			ID:    "tenant.requests",
			Kind:  authoritydomain.RuleKindQuota,
			Mode:  authoritydomain.RuleModeStrict,
			Unit:  authoritydomain.AmountUnitRequests,
			Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitRequests, Value: 10},
			Window: authoritydomain.WindowSpec{
				Algorithm: authoritydomain.WindowAlgorithmFixed,
				Size:      time.Hour,
				Anchor:    anchor,
			},
			Match: authoritydomain.DimensionsMatcher{
				Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")},
			},
		},
	}

	rows, err := authoritystore.LimitRowsFromRules(rules, at)
	if err != nil {
		t.Fatalf("LimitRowsFromRules: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.RuleID != "tenant.requests" || row.RuleType != "quota" {
		t.Fatalf("rule metadata = %#v", row)
	}
	if row.Unit != string(authoritydomain.AmountUnitRequests) || row.Limit != 10 || row.Remaining != 10 {
		t.Fatalf("limit totals = %#v", row)
	}
	if row.Correlation.BackendID != "backend-1" || row.Correlation.Model != "" {
		t.Fatalf("correlation = %#v", row.Correlation)
	}
	if !row.Scope.TenantID.IsUnknown() {
		t.Fatalf("wildcard tenant must remain unknown: %#v", row.Scope.TenantID)
	}
	if row.Authority != controlplane.AccountingAuthoritySourceAuthoritative {
		t.Fatalf("authority = %q, want authoritative", row.Authority)
	}
	if !row.WindowStart.Equal(anchor) || !row.WindowEnd.Equal(anchor.Add(time.Hour)) {
		t.Fatalf("window bounds = %s..%s, want %s..%s", row.WindowStart, row.WindowEnd, anchor, anchor.Add(time.Hour))
	}
}

func TestLimitRowsFromRulesWildcardRowMatchesKnownRequestDimensions(t *testing.T) {
	t.Parallel()

	rules := []authoritydomain.Rule{
		{
			ID:    "tenant.requests",
			Kind:  authoritydomain.RuleKindQuota,
			Mode:  authoritydomain.RuleModeStrict,
			Unit:  authoritydomain.AmountUnitRequests,
			Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitRequests, Value: 10},
			Match: authoritydomain.DimensionsMatcher{
				Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")},
			},
		},
	}
	rows, err := authoritystore.LimitRowsFromRules(rules, time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("LimitRowsFromRules: %v", err)
	}

	store := authoritystore.NewMemory(authoritystore.Config{
		Backing:   authoritydomain.BackingCapabilityAtomic,
		Readiness: authoritydomain.StatusFromBacking(authoritydomain.BackingCapabilityAtomic),
		LimitRows: rows,
	})

	cmd := strictReserveCommandForPersistence()
	cmd.RuleID = "tenant.requests"
	cmd.ReservationKey.RuleID = "tenant.requests"
	cmd.Dimensions = authoritydomain.Dimensions{
		Principal: scope.Known("principal-1"),
		Tenant:    scope.Known("tenant-1"),
		Backend:   scope.Known("backend-1"),
		Model:     scope.Known("model-1"),
	}
	cmd.Request = authoritydomain.Amount{Unit: authoritydomain.AmountUnitRequests, Value: 1}
	cmd.At = time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	got, err := store.Reserve(t.Context(), cmd)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if !got.Applied {
		t.Fatalf("Reserve must apply against seeded wildcard row: %#v", got)
	}
}

// TestLimitRowsFromRulesSeedsCredentialIntoScopeSnapshot pins requirement 1.2:
// a rule whose matcher targets the credential dimension must seed a live limit
// row whose scope snapshot carries that credential, so later reserve requests
// are matched against the credential dimension. An unconfigured credential
// matcher must seed an unknown (wildcard) credential.
func TestLimitRowsFromRulesSeedsCredentialIntoScopeSnapshot(t *testing.T) {
	t.Parallel()

	rules := []authoritydomain.Rule{
		{
			ID:    "tenant.credential",
			Kind:  authoritydomain.RuleKindQuota,
			Mode:  authoritydomain.RuleModeStrict,
			Unit:  authoritydomain.AmountUnitRequests,
			Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitRequests, Value: 10},
			Match: authoritydomain.DimensionsMatcher{
				Credential: authoritydomain.DimensionMatcher{Value: scope.Known("cred-1")},
			},
		},
	}
	rows, err := authoritystore.LimitRowsFromRules(rules, time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("LimitRowsFromRules: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if !row.Scope.CredentialID.Equal(scope.Known("cred-1")) {
		t.Fatalf("scope credential = %v, want cred-1", row.Scope.CredentialID)
	}
	if !row.Scope.Principal.CredentialID.Equal(scope.Known("cred-1")) {
		t.Fatalf("principal scope credential = %v, want cred-1", row.Scope.Principal.CredentialID)
	}
}

// TestLimitRowsFromRulesUnconfiguredCredentialStaysUnknown guards backward
// compat: a rule that does not configure a credential matcher must seed an
// unknown (wildcard) credential so existing rules still match any credential.
func TestLimitRowsFromRulesUnconfiguredCredentialStaysUnknown(t *testing.T) {
	t.Parallel()

	rules := []authoritydomain.Rule{
		{
			ID:    "tenant.requests",
			Kind:  authoritydomain.RuleKindQuota,
			Mode:  authoritydomain.RuleModeStrict,
			Unit:  authoritydomain.AmountUnitRequests,
			Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitRequests, Value: 10},
			Match: authoritydomain.DimensionsMatcher{
				Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")},
			},
		},
	}
	rows, err := authoritystore.LimitRowsFromRules(rules, time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("LimitRowsFromRules: %v", err)
	}
	if !rows[0].Scope.CredentialID.IsUnknown() {
		t.Fatalf("unconfigured credential should seed unknown, got %v", rows[0].Scope.CredentialID)
	}
}

// TestScopeDimensionsMatchHonorsCredential pins requirement 1.2: the resolver
// path must honor the credential dimension when matching a stored limit row's
// scope against incoming request dimensions. A known stored credential matches
// only an equal request credential; an unknown stored credential remains a
// wildcard (backward compat).
func TestScopeDimensionsMatchHonorsCredential(t *testing.T) {
	t.Parallel()

	knownCred := controlplane.ScopeSnapshot{
		CredentialID: scope.Known("cred-1"),
	}
	if !authoritystore.ScopeDimensionsMatch(knownCred, authoritydomain.Dimensions{Credential: scope.Known("cred-1")}) {
		t.Fatal("known credential should match equal request credential")
	}
	if authoritystore.ScopeDimensionsMatch(knownCred, authoritydomain.Dimensions{Credential: scope.Known("cred-2")}) {
		t.Fatal("known credential should not match a different request credential")
	}
	if authoritystore.ScopeDimensionsMatch(knownCred, authoritydomain.Dimensions{Credential: scope.Unknown()}) {
		t.Fatal("known credential should not match an unknown request credential")
	}

	wildcard := controlplane.ScopeSnapshot{
		CredentialID: scope.Unknown(),
	}
	if !authoritystore.ScopeDimensionsMatch(wildcard, authoritydomain.Dimensions{Credential: scope.Known("any")}) {
		t.Fatal("unknown (wildcard) stored credential should match any request credential")
	}
	if !authoritystore.ScopeDimensionsMatch(wildcard, authoritydomain.Dimensions{Credential: scope.Unknown()}) {
		t.Fatal("unknown (wildcard) stored credential should match unknown request credential")
	}
}

func TestScopeDimensionsMatchPreservesPolicyLabelPresence(t *testing.T) {
	t.Parallel()

	row := controlplane.ScopeSnapshot{Principal: scope.PrincipalScopeView{
		PolicyLabels: map[string]string{"tier": ""},
	}}
	knownEmpty := authoritydomain.Dimensions{PolicyLabels: map[string]scope.Value{"tier": scope.Known("")}}
	if !authoritystore.ScopeDimensionsMatch(row, knownEmpty) {
		t.Fatal("known-empty policy label should match known-empty stored label")
	}
	knownOther := authoritydomain.Dimensions{PolicyLabels: map[string]scope.Value{"tier": scope.Known("gold")}}
	if authoritystore.ScopeDimensionsMatch(row, knownOther) {
		t.Fatal("different known policy label should not match")
	}
	unknown := authoritydomain.Dimensions{PolicyLabels: map[string]scope.Value{"tier": scope.Unknown()}}
	if authoritystore.ScopeDimensionsMatch(row, unknown) {
		t.Fatal("explicitly unknown policy label should not match a known stored label")
	}
	if authoritystore.ScopeDimensionsMatch(row, authoritydomain.Dimensions{}) {
		t.Fatal("absent request labels must not match a stored label requirement")
	}

	goldRow := controlplane.ScopeSnapshot{Principal: scope.PrincipalScopeView{
		PolicyLabels: map[string]string{"tier": "gold"},
	}}
	if authoritystore.ScopeDimensionsMatch(goldRow, authoritydomain.Dimensions{}) {
		t.Fatal("stored tier=gold must not match empty request dimensions")
	}
	if !authoritystore.ScopeDimensionsMatch(goldRow, authoritydomain.Dimensions{
		PolicyLabels: map[string]scope.Value{"tier": scope.Known("gold")},
	}) {
		t.Fatal("stored tier=gold should match known gold request label")
	}
	wildcard := controlplane.ScopeSnapshot{Principal: scope.PrincipalScopeView{}}
	if !authoritystore.ScopeDimensionsMatch(wildcard, authoritydomain.Dimensions{}) {
		t.Fatal("empty stored policy label map remains a wildcard")
	}
}

func TestMemoryStoreLimitRowsKeepPolicyLabelDimensionsDistinct(t *testing.T) {
	t.Parallel()

	rules := []authoritydomain.Rule{
		{
			ID:    "labels.quota",
			Kind:  authoritydomain.RuleKindQuota,
			Mode:  authoritydomain.RuleModeStrict,
			Unit:  authoritydomain.AmountUnitRequests,
			Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitRequests, Value: 10},
			Match: authoritydomain.DimensionsMatcher{Labels: map[string]authoritydomain.DimensionMatcher{
				"tier": {Value: scope.Known("gold")},
			}},
		},
		{
			ID:    "labels.quota",
			Kind:  authoritydomain.RuleKindQuota,
			Mode:  authoritydomain.RuleModeStrict,
			Unit:  authoritydomain.AmountUnitRequests,
			Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitRequests, Value: 20},
			Match: authoritydomain.DimensionsMatcher{Labels: map[string]authoritydomain.DimensionMatcher{
				"tier": {Value: scope.Known("silver")},
			}},
		},
	}
	rows, err := authoritystore.LimitRowsFromRules(rules, time.Now().UTC())
	if err != nil {
		t.Fatalf("LimitRowsFromRules: %v", err)
	}
	store := authoritystore.NewMemory(authoritystore.Config{
		StoreID:   "labels-distinct",
		Backing:   authoritydomain.BackingCapabilityAtomic,
		Readiness: authoritydomain.AuthorityStatus{State: authoritydomain.AuthorityStateReady, Reason: authoritydomain.StatusReasonNone},
		LimitRows: rows,
	})
	page, err := store.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{
		RuleID: "labels.quota", Unit: string(authoritydomain.AmountUnitRequests), Limit: 10,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("LimitStatus: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("label-specific limit rows = %d, want 2: %#v", len(page.Items), page.Items)
	}
}
