package app_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

type staticRules struct{ snap app.RuleSnapshot }

func (s staticRules) Snapshot(context.Context) (app.RuleSnapshot, error) { return s.snap, nil }

// memoryStore is a test-only LeaseStore fake (production dialects are task 8.2).
type memoryStore struct {
	mu     sync.Mutex
	leases map[string]domain.Lease
	ready  domain.Readiness
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		leases: make(map[string]domain.Lease),
		ready:  domain.Readiness{State: domain.ReadinessStateReady},
	}
}

func (s *memoryStore) Acquire(_ context.Context, cmd app.AcquireCommand) (app.AcquireResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.leases[cmd.Lease.LeaseID]; ok {
		if existing.IsLive(cmd.Now) {
			return app.AcquireResult{
				Lease:          existing,
				Replayed:       true,
				RemainingSlots: remaining(s, cmd, cmd.Now),
			}, nil
		}
		// Expired/released rows do not occupy capacity; overwrite on re-acquire.
	}

	live := 0
	for _, l := range s.leases {
		if l.RuleID == cmd.RuleID && l.Dimensions.Key() == cmd.Dimensions.Key() && l.IsLive(cmd.Now) {
			live++
		}
	}
	if live >= cmd.Limit {
		return app.AcquireResult{
			CapacityExceeded: true,
			RemainingSlots:   0,
		}, nil
	}

	lease := cmd.Lease
	s.leases[lease.LeaseID] = lease
	return app.AcquireResult{
		Lease:          lease,
		RemainingSlots: cmd.Limit - live - 1,
	}, nil
}

func remaining(s *memoryStore, cmd app.AcquireCommand, now time.Time) int {
	live := 0
	for _, l := range s.leases {
		if l.RuleID == cmd.RuleID && l.Dimensions.Key() == cmd.Dimensions.Key() && l.IsLive(now) {
			live++
		}
	}
	left := cmd.Limit - live
	if left < 0 {
		return 0
	}
	return left
}

func (s *memoryStore) Renew(_ context.Context, cmd app.RenewCommand) (app.RenewResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.leases[cmd.LeaseID]
	if !ok {
		return app.RenewResult{}, app.ErrNotFound
	}
	if err := lease.Renew(cmd.Now, cmd.ExpectedGeneration, cmd.TTL); err != nil {
		return app.RenewResult{}, err
	}
	s.leases[cmd.LeaseID] = lease
	return app.RenewResult{Lease: lease}, nil
}

func (s *memoryStore) Release(_ context.Context, cmd app.ReleaseCommand) (app.ReleaseResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.leases[cmd.LeaseID]
	if !ok {
		return app.ReleaseResult{Applied: false}, nil
	}
	lease.Release(cmd.Now)
	s.leases[cmd.LeaseID] = lease
	return app.ReleaseResult{Applied: true, Lease: lease}, nil
}

func (s *memoryStore) Query(_ context.Context, q app.QueryCommand) (app.QueryResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Lease
	for _, l := range s.leases {
		if q.LeaseID != "" && l.LeaseID != q.LeaseID {
			continue
		}
		if q.RequestID != "" && l.LogicalID != q.RequestID {
			continue
		}
		state := l.EffectiveState(q.Now)
		if q.State != "" && state != q.State {
			continue
		}
		cp := l
		cp.State = state
		out = append(out, cp)
	}
	return app.QueryResult{Leases: out}, nil
}

func (s *memoryStore) CheckReadiness(context.Context) (domain.Readiness, error) {
	return s.ready, nil
}

func principalScope(id string) scope.PrincipalScopeView {
	return scope.PrincipalScopeView{PrincipalID: scope.Known(id), Origin: scope.OriginClient}
}

func strictRule(limit int) domain.Rule {
	return domain.Rule{
		ID:        "max-active",
		Namespace: "default",
		Version:   "v1",
		Mode:      domain.RuleModeStrict,
		Limit:     limit,
		LeaseTTL:  time.Minute,
		Match: domain.DimensionsMatcher{
			Principal: domain.DimensionMatcher{Value: scope.Known("alice")},
		},
	}
}

func advisoryRule(limit int) domain.Rule {
	r := strictRule(limit)
	r.ID = "max-active-adv"
	r.Mode = domain.RuleModeAdvisory
	return r
}

func newService(t *testing.T, rules []domain.Rule, store *memoryStore, now time.Time) *app.Service {
	t.Helper()
	return app.NewService(staticRules{snap: app.RuleSnapshot{
		Readiness: domain.Readiness{State: domain.ReadinessStateReady},
		Rules:     rules,
	}}, store, fixedClock{t: now})
}

func TestAdmit_StrictDenyVsAdvisory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	t.Run("strict denies at capacity", func(t *testing.T) {
		t.Parallel()
		store := newMemoryStore()
		svc := newService(t, []domain.Rule{strictRule(1)}, store, now)

		first, err := svc.Admit(ctx, app.AdmitInput{
			RequestID: "req-1",
			Scope:     principalScope("alice"),
			Namespace: "default",
		})
		if err != nil {
			t.Fatal(err)
		}
		if first.Kind != domain.DecisionAllow || first.LeaseID == "" {
			t.Fatalf("first=%+v", first)
		}

		second, err := svc.Admit(ctx, app.AdmitInput{
			RequestID: "req-2",
			Scope:     principalScope("alice"),
			Namespace: "default",
		})
		if err != nil {
			t.Fatal(err)
		}
		if second.Kind != domain.DecisionDeny {
			t.Fatalf("kind=%s want deny", second.Kind)
		}
		if second.Evidence.Category != domain.EvidenceCategoryConcurrencyLimit {
			t.Fatalf("evidence=%+v", second.Evidence)
		}
		if second.Evidence.Message != "" && (strings.Contains(second.Evidence.Message, first.LeaseID) || strings.Contains(second.Evidence.Message, "alice")) {
			t.Fatalf("unsafe evidence message: %q", second.Evidence.Message)
		}
	})

	t.Run("advisory allows when over limit", func(t *testing.T) {
		t.Parallel()
		store := newMemoryStore()
		svc := newService(t, []domain.Rule{advisoryRule(1)}, store, now)

		first, err := svc.Admit(ctx, app.AdmitInput{
			RequestID: "req-1", Scope: principalScope("alice"), Namespace: "default",
		})
		if err != nil {
			t.Fatal(err)
		}
		if first.Kind != domain.DecisionAllow {
			t.Fatalf("first=%+v", first)
		}
		second, err := svc.Admit(ctx, app.AdmitInput{
			RequestID: "req-2", Scope: principalScope("alice"), Namespace: "default",
		})
		if err != nil {
			t.Fatal(err)
		}
		if second.Kind != domain.DecisionAdvisory {
			t.Fatalf("kind=%s want advisory", second.Kind)
		}
	})
}

func TestAdmit_SafeScopeMatching(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	svc := newService(t, []domain.Rule{strictRule(1)}, store, now)

	bob, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-bob", Scope: principalScope("bob"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if bob.Kind != domain.DecisionAllow || bob.LeaseID != "" {
		t.Fatalf("unmatched principal should skip rules: %+v", bob)
	}
}

func TestAdmit_IdempotentReplaySameLogicalRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	svc := newService(t, []domain.Rule{strictRule(1)}, store, now)

	first, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-same", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Retry / parallel leg: same logical request id must replay one lease.
	retry, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-same", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.LeaseID == "" || first.LeaseID != retry.LeaseID {
		t.Fatalf("replay lease mismatch: %q vs %q", first.LeaseID, retry.LeaseID)
	}
	if first.Generation != retry.Generation {
		t.Fatalf("generation drift on replay: %d vs %d", first.Generation, retry.Generation)
	}
	// Capacity still one: a different request must deny.
	other, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-other", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if other.Kind != domain.DecisionDeny {
		t.Fatalf("expected deny for second distinct request, got %+v", other)
	}
}

func TestRenew_TTLGenerationCASAndNoResurrect(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	svc := newService(t, []domain.Rule{strictRule(2)}, store, now)

	admitted, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-1", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}

	renewed, err := svc.Renew(ctx, app.RenewInput{
		LeaseID:            admitted.LeaseID,
		RequestID:          "req-1",
		ExpectedGeneration: admitted.Generation,
		TTL:                time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Generation != admitted.Generation+1 {
		t.Fatalf("generation=%d want %d", renewed.Generation, admitted.Generation+1)
	}
	if !renewed.ExpiresAt.After(now) {
		t.Fatalf("expires_at not extended: %v", renewed.ExpiresAt)
	}

	if err := svc.Release(ctx, app.ReleaseInput{LeaseID: admitted.LeaseID, RequestID: "req-1"}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Renew(ctx, app.RenewInput{
		LeaseID:            admitted.LeaseID,
		RequestID:          "req-1",
		ExpectedGeneration: renewed.Generation,
		TTL:                time.Minute,
	})
	if err == nil {
		t.Fatal("renew after release must fail")
	}
}

func TestReleaseAndExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	clock := &mutableClock{t: now}
	svc := app.NewService(staticRules{snap: app.RuleSnapshot{
		Readiness: domain.Readiness{State: domain.ReadinessStateReady},
		Rules:     []domain.Rule{strictRule(1)},
	}}, store, clock)

	admitted, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-1", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Release(ctx, app.ReleaseInput{LeaseID: admitted.LeaseID, RequestID: "req-1"}); err != nil {
		t.Fatal(err)
	}
	// Idempotent release.
	if err := svc.Release(ctx, app.ReleaseInput{LeaseID: admitted.LeaseID, RequestID: "req-1"}); err != nil {
		t.Fatal(err)
	}
	// Slot free after release.
	again, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-2", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Kind != domain.DecisionAllow {
		t.Fatalf("expected allow after release, got %+v", again)
	}

	// Expiry frees capacity without explicit release.
	store2 := newMemoryStore()
	clock2 := &mutableClock{t: now}
	svc2 := app.NewService(staticRules{snap: app.RuleSnapshot{
		Readiness: domain.Readiness{State: domain.ReadinessStateReady},
		Rules:     []domain.Rule{strictRule(1)},
	}}, store2, clock2)
	expiring, err := svc2.Admit(ctx, app.AdmitInput{
		RequestID: "req-exp", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil || expiring.Kind != domain.DecisionAllow {
		t.Fatalf("admit: %+v err=%v", expiring, err)
	}
	clock2.t = now.Add(2 * time.Minute)
	next, err := svc2.Admit(ctx, app.AdmitInput{
		RequestID: "req-after-exp", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Kind != domain.DecisionAllow {
		t.Fatalf("expired lease must free capacity: %+v", next)
	}
}

type mutableClock struct{ t time.Time }

func (c *mutableClock) Now() time.Time { return c.t }

func TestReadinessReporting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	store.ready = domain.Readiness{State: domain.ReadinessStateDegraded, Reason: "backing_degraded"}
	svc := app.NewService(staticRules{snap: app.RuleSnapshot{
		Readiness: domain.Readiness{State: domain.ReadinessStateDegraded, Reason: "backing_degraded"},
		Rules:     []domain.Rule{strictRule(5)},
	}}, store, fixedClock{t: now})

	got, err := svc.Readiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != authority.ReadinessDegraded {
		t.Fatalf("readiness=%s", got)
	}
	decision, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-1", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Readiness.State != domain.ReadinessStateDegraded {
		t.Fatalf("decision readiness=%s", decision.Readiness.State)
	}
}

func TestAuxInheritsParentNoSecondLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	svc := newService(t, []domain.Rule{strictRule(1)}, store, now)

	parent, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-parent", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}

	aux, err := svc.Admit(ctx, app.AdmitInput{
		RequestID:     "req-aux",
		Scope:         principalScope("alice"),
		Namespace:     "default",
		Lifecycle:     metering.LifecycleAuxiliaryRequest,
		ParentLeaseID: parent.LeaseID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if aux.Kind != domain.DecisionAllow {
		t.Fatalf("aux=%+v", aux)
	}
	if aux.LeaseID != parent.LeaseID {
		t.Fatalf("aux should inherit parent lease: got %q want %q", aux.LeaseID, parent.LeaseID)
	}
	if aux.Acquired {
		t.Fatal("aux must not acquire a second top-level lease by default")
	}

	// Capacity still held by parent only — another top-level request denies.
	other, err := svc.Admit(ctx, app.AdmitInput{
		RequestID: "req-other", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if other.Kind != domain.DecisionDeny {
		t.Fatalf("expected deny, got %+v", other)
	}

	// Configurable: acquire own lease for aux.
	store2 := newMemoryStore()
	svc2 := newService(t, []domain.Rule{strictRule(2)}, store2, now)
	p2, err := svc2.Admit(ctx, app.AdmitInput{
		RequestID: "req-parent", Scope: principalScope("alice"), Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	own, err := svc2.Admit(ctx, app.AdmitInput{
		RequestID:     "req-aux-own",
		Scope:         principalScope("alice"),
		Namespace:     "default",
		Lifecycle:     metering.LifecycleAuxiliaryRequest,
		ParentLeaseID: p2.LeaseID,
		AuxPolicy:     domain.AuxPolicyAcquireOwn,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !own.Acquired || own.LeaseID == "" || own.LeaseID == p2.LeaseID {
		t.Fatalf("expected distinct aux lease: %+v parent=%q", own, p2.LeaseID)
	}
}

func TestProviderAdapter_ImplementsConcurrencyProvider(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	svc := newService(t, []domain.Rule{strictRule(2)}, store, now)
	var provider authority.ConcurrencyProvider = app.NewProvider(svc)

	dec, err := provider.AdmitLease(ctx, authority.LeaseAdmission{
		RequestID: "req-1",
		Scope:     principalScope("alice"),
		Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != authority.LeaseAllow || dec.LeaseID == "" {
		t.Fatalf("%+v", dec)
	}
	renewed, err := provider.RenewLease(ctx, authority.LeaseRenew{
		LeaseID:            dec.LeaseID,
		RequestID:          "req-1",
		ExpectedGeneration: dec.Generation,
		TTL:                time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Generation != dec.Generation+1 {
		t.Fatalf("generation=%d", renewed.Generation)
	}
	if err := provider.ReleaseLease(ctx, authority.LeaseRelease{LeaseID: dec.LeaseID, RequestID: "req-1"}); err != nil {
		t.Fatal(err)
	}
	page, err := provider.QueryLeases(ctx, authority.LeaseQuery{LeaseID: dec.LeaseID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Leases) != 1 || page.Leases[0].State != authority.LeaseStateReleased {
		t.Fatalf("query=%+v", page)
	}
}
