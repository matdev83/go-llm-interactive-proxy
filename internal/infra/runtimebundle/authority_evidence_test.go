package runtimebundle_test

import (
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/controlplane/ledgerstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// authorityAndControlPlaneConfig enables both the usage-authority capability
// (memory, strict) and the control-plane event ledger (memory, best_effort) so
// authority decisions project into both evidence surfaces. authorityLimit
// controls the single quota rule's limit so tests can drive allow vs deny.
func authorityAndControlPlaneConfig(t *testing.T, authorityLimit int64, controlPlaneEnabled bool) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Server:      config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:     config.RoutingConfig{DefaultRoute: "stub:model", MaxAttempts: 3},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{SharedSecret: strings.Repeat("s", 12)},
		Plugins:     config.PluginsConfig{},
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled:        true,
				Mode:           "strict",
				Store:          "memory",
				StartupPosture: "fail_closed",
				Query:          config.AccountingAuthorityQueryConfig{Enabled: false},
				Rules: []config.AccountingAuthorityRuleConfig{
					{
						ID:    "tenant.requests",
						Kind:  "quota",
						Mode:  "strict",
						Unit:  "requests",
						Limit: authorityLimit,
						Match: config.AccountingAuthorityDimensionsConfig{
							Backend: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("backend-1")},
						},
					},
				},
			},
		},
	}
	if controlPlaneEnabled {
		cfg.ControlPlane = config.ControlPlaneConfig{
			Enabled:         true,
			Store:           "memory",
			RecordingPolicy: "best_effort",
		}
	}
	return cfg
}

// accountingAuthorityEvents returns the CategoryAccountingAuthority events
// recorded in the control-plane store, proving the authority EvidenceSink
// projected its dedicated detail event (distinct from CategoryPolicy events
// produced by the policy observer adapter).
func accountingAuthorityEvents(t *testing.T, store *ledgerstore.MemoryStore) []cp.Event {
	t.Helper()
	page, err := store.Events(context.Background(), cp.EventQuery{Limit: 100, Visibility: cp.VisibilityDefault})
	if err != nil {
		t.Fatalf("control-plane Events query: %v", err)
	}
	var out []cp.Event
	for _, ev := range page.Items {
		if ev.Category == cp.CategoryAccountingAuthority {
			out = append(out, ev)
		}
	}
	return out
}

func firstAuthorityRuleID(res authorityapp.AdmissionResult, fallback string) string {
	if len(res.RuleIDs) > 0 {
		return res.RuleIDs[0]
	}
	return fallback
}

// TestUsageAuthority_EvidenceProjectedThroughRealBundle proves the production
// EvidenceSink wiring: an allowed admission through the real runtimebundle
// projects a CategoryAccountingAuthority event into the control-plane ledger
// and a policydecision.Record into the policy observer chain, and a subsequent
// settlement projects settlement evidence. This is the gate that the original
// nil-evidence wiring failed to provide.
func TestUsageAuthority_EvidenceProjectedThroughRealBundle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cpStore, err := ledgerstore.NewMemoryStore(ledgerstore.MemoryConfig{StoreID: "authority-evidence-allow"})
	if err != nil {
		t.Fatalf("control-plane memory store: %v", err)
	}
	defer func() { _ = cpStore.Close() }()

	captured := &capturePolicyObserver{}
	cfg := authorityAndControlPlaneConfig(t, 10, true)
	opts := baseAuthorityOptions(t, nil)
	opts.Testing.ControlPlaneStoreOverride = cpStore
	opts.Policy.PolicyObservers = []policydecision.Observer{captured}

	_, built := mustProcessAndCandidate(t, cfg, opts)
	if runtimebundle.CandidateUsageAuthority(built) == nil {
		t.Fatal("expected usage authority service when enabled")
	}

	admitIn := authorityAdmissionInput()
	admitRes, err := runtimebundle.CandidateUsageAuthority(built).Admit(ctx, admitIn)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !admitRes.Allowed {
		t.Fatalf("expected allow against Limit=10, got %#v", admitRes)
	}

	allowEvs := accountingAuthorityEvents(t, cpStore)
	if len(allowEvs) != 1 {
		t.Fatalf("accounting-authority events after allow = %d, want 1", len(allowEvs))
	}
	if allowEvs[0].AccountingAuthority() == nil {
		t.Fatal("expected AccountingAuthority detail on the allow event")
	}
	if allowEvs[0].AccountingAuthority().Outcome != cp.AccountingOutcomeAllow {
		t.Fatalf("allow event outcome = %q, want %q", allowEvs[0].AccountingAuthority().Outcome, cp.AccountingOutcomeAllow)
	}

	recs := captured.snapshot()
	if len(recs) != 1 {
		t.Fatalf("policy observer records after allow = %d, want 1", len(recs))
	}

	// Settle the reservation and assert a second accounting-authority event
	// (settlement projection) is appended.
	settleIn := authorityapp.SettleInput{
		Correlation:    admitIn.Correlation,
		Scope:          admitIn.Scope,
		ReservationKey: admitIn.ReservationKey,
		ReservationID:  admitRes.ReservationID,
		RuleID:         firstAuthorityRuleID(admitRes, admitIn.ReservationKey.RuleID),
		Kind:           authorityapp.SettlementKindFinal,
		FinalUsage:     admitRes.ReservedAmount,
		ReservedUsage:  admitRes.ReservedAmount,
		EstimatedUsage: admitIn.Request,
		Authority:      authoritydomain.AuthorityLevelAuthoritative,
	}
	if _, err := runtimebundle.CandidateUsageAuthority(built).Settle(ctx, settleIn); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	settleEvs := accountingAuthorityEvents(t, cpStore)
	if len(settleEvs) != 2 {
		t.Fatalf("accounting-authority events after settle = %d, want 2", len(settleEvs))
	}
	if settleEvs[1].AccountingAuthority() == nil {
		t.Fatal("expected AccountingAuthority detail on the settlement event")
	}
	if got := settleEvs[1].AccountingAuthority().SettlementState; got != cp.AccountingSettlementSettled {
		t.Fatalf("settlement event state = %q, want %q", got, cp.AccountingSettlementSettled)
	}
	if recs2 := captured.snapshot(); len(recs2) != 2 {
		t.Fatalf("policy observer records after settle = %d, want 2", len(recs2))
	}
}

// TestUsageAuthority_DenialEvidenceProjectedThroughRealBundle proves a strict
// denial (Limit=0) still projects a CategoryAccountingAuthority denial event
// and a policydecision denial record, so operators can explain enforcement
// outcomes (requirement 9.1, 9.3).
func TestUsageAuthority_DenialEvidenceProjectedThroughRealBundle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cpStore, err := ledgerstore.NewMemoryStore(ledgerstore.MemoryConfig{StoreID: "authority-evidence-deny"})
	if err != nil {
		t.Fatalf("control-plane memory store: %v", err)
	}
	defer func() { _ = cpStore.Close() }()

	captured := &capturePolicyObserver{}
	cfg := authorityAndControlPlaneConfig(t, 0, true)
	opts := baseAuthorityOptions(t, nil)
	opts.Testing.ControlPlaneStoreOverride = cpStore
	opts.Policy.PolicyObservers = []policydecision.Observer{captured}

	_, built := mustProcessAndCandidate(t, cfg, opts)

	admitRes, err := runtimebundle.CandidateUsageAuthority(built).Admit(ctx, authorityAdmissionInput())
	if err != nil {
		t.Fatalf("Admit returned error on deny (expected result with Allowed=false): %v", err)
	}
	if admitRes.Allowed {
		t.Fatalf("expected deny against Limit=0, got %#v", admitRes)
	}

	denyEvs := accountingAuthorityEvents(t, cpStore)
	if len(denyEvs) != 1 {
		t.Fatalf("accounting-authority events after deny = %d, want 1", len(denyEvs))
	}
	if denyEvs[0].AccountingAuthority() == nil {
		t.Fatal("expected AccountingAuthority detail on the deny event")
	}
	if denyEvs[0].AccountingAuthority().Outcome != cp.AccountingOutcomeDeny {
		t.Fatalf("deny event outcome = %q, want %q", denyEvs[0].AccountingAuthority().Outcome, cp.AccountingOutcomeDeny)
	}
	if recs := captured.snapshot(); len(recs) != 1 {
		t.Fatalf("policy observer records after deny = %d, want 1", len(recs))
	}
}

// TestUsageAuthority_EvidenceFansToObserversWhenControlPlaneDisabled proves
// that when the control-plane ledger is disabled but operator policy observers
// are configured, authority decisions still fan to the operator observer chain
// (full-chain routing), while the accounting-authority event projection is a
// no-op because there is no recorder. Authority enforcement is unaffected.
func TestUsageAuthority_EvidenceFansToObserversWhenControlPlaneDisabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	captured := &capturePolicyObserver{}
	cfg := authorityAndControlPlaneConfig(t, 10, false)
	opts := baseAuthorityOptions(t, nil)
	opts.Policy.PolicyObservers = []policydecision.Observer{captured}

	_, built := mustProcessAndCandidate(t, cfg, opts)
	if runtimebundle.CandidateUsageAuthority(built) == nil {
		t.Fatal("expected usage authority service when enabled")
	}

	admitRes, err := runtimebundle.CandidateUsageAuthority(built).Admit(ctx, authorityAdmissionInput())
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !admitRes.Allowed {
		t.Fatalf("expected allow against Limit=10 with control-plane disabled, got %#v", admitRes)
	}
	if recs := captured.snapshot(); len(recs) != 1 {
		t.Fatalf("operator policy observer records = %d, want 1 (full chain must reach operators even without control-plane)", len(recs))
	}
}
