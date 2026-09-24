package lipruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// --- Binding-port fakes (pointer receivers: typed-nil boxable). ---

type billingScreen struct {
	result billing.CreditScreenResult
	err    error
}

func (f *billingScreen) Check(context.Context, billing.CreditScreenInput) (billing.CreditScreenResult, error) {
	return f.result, f.err
}

type billingQuoter struct {
	quote economics.ExposureQuote
	err   error
}

func (f *billingQuoter) Quote(context.Context, economics.QuoteInput) (economics.ExposureQuote, error) {
	return f.quote, f.err
}

type billingAdmitter struct {
	handle billing.ExposureHandle
	err    error
}

func (f *billingAdmitter) Admit(context.Context, billing.ExposureAdmissionInput) (billing.ExposureHandle, error) {
	return f.handle, f.err
}

type billingTerminal struct {
	err error
}

func (f *billingTerminal) AppendTerminal(_ context.Context, env billing.TerminalEnvelope) (billing.TerminalAck, error) {
	if f == nil {
		return billing.TerminalAck{}, errors.New("nil terminal")
	}
	if f.err != nil {
		return billing.TerminalAck{}, f.err
	}
	// Answer the actual input envelope so the acknowledgement binds to
	// what was sent.
	return billing.TerminalAck{StoreID: env.StoreID, EnvelopeID: env.EnvelopeID, Revision: 1}, nil
}

type lifecycleCounters struct {
	starts map[string]int
	closes map[string]int
}

func (c *lifecycleCounters) owned(id string, startErr error) billing.OwnedResource {
	if c.starts == nil {
		c.starts = map[string]int{}
		c.closes = map[string]int{}
	}
	return billing.OwnedResource{
		ID:    id,
		Start: func(context.Context) error { c.starts[id]++; return startErr },
		Close: func(context.Context) error { c.closes[id]++; return nil },
	}
}

func validFacadeBinding(counters *lifecycleCounters) billing.Binding {
	return billing.Binding{
		ID:      "facade-billing",
		Version: billing.BindingVersionV1,
		Scope: billing.BindingScope{
			StoreID: "store-test",
			Tariff:  economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-snap", Version: "v1"}},
			Policy:  economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-snap", Version: "v1"}, PolicyID: "policy-1"},
		},
		CreditScreen: &billingScreen{result: billing.CreditScreenResult{Decision: billing.CreditAllow, StoreID: "store-test", AccountID: "acct-1"}},
		Quoter:       &billingQuoter{},
		Admission:    &billingAdmitter{handle: billing.ExposureHandle{StoreID: "store-test", BillingCallID: "call-1", ExposureID: "exp-1", QuoteID: "q-1"}},
		Terminal:     &billingTerminal{},
		Lifecycle: billing.Lifecycle{
			Owned:    []billing.OwnedResource{counters.owned("worker-1", nil)},
			Borrowed: []billing.BorrowedRef{{ID: "store-shared", Kind: "journal"}},
		},
	}
}

func TestBuildWithBillingBuildsReadyHostOnSharedPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	counters := &lifecycleCounters{}
	rt, err := lipruntime.BuildWithBilling(ctx, lipruntime.Options{ConfigPath: repoConfigPath(t)}, validFacadeBinding(counters))
	if err != nil {
		t.Fatalf("BuildWithBilling: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if !rt.Ready() {
		t.Fatal("expected ready runtime from BuildWithBilling")
	}
	if rt.ExecutorView() == nil {
		t.Fatal("expected ExecutorView from BuildWithBilling")
	}
	if counters.starts["worker-1"] != 1 {
		t.Fatalf("owned starts = %d, want exactly 1", counters.starts["worker-1"])
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if counters.closes["worker-1"] != 1 {
		t.Fatalf("owned closes = %d, want exactly 1 after Close", counters.closes["worker-1"])
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if counters.closes["worker-1"] != 1 {
		t.Fatalf("owned closes after repeated Close = %d, want still 1", counters.closes["worker-1"])
	}
}

func TestBuildWithBillingRejectsInvalidBindingBeforeStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	counters := &lifecycleCounters{}
	bad := validFacadeBinding(counters)
	bad.CreditScreen = nil
	if _, err := lipruntime.BuildWithBilling(ctx, lipruntime.Options{ConfigPath: repoConfigPath(t)}, bad); err == nil {
		t.Fatal("nil credit screen must be rejected")
	}
	if counters.starts["worker-1"] != 0 {
		t.Fatal("owned resources must not start when binding validation fails")
	}
	bad = validFacadeBinding(counters)
	bad.Version = 0
	if _, err := lipruntime.BuildWithBilling(ctx, lipruntime.Options{ConfigPath: repoConfigPath(t)}, bad); err == nil {
		t.Fatal("bad version must be rejected")
	}
}

func TestBuildWithBillingRejectsTypedNilBindingMember(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	counters := &lifecycleCounters{}
	bad := validFacadeBinding(counters)
	bad.Terminal = (*billingTerminal)(nil)
	if _, err := lipruntime.BuildWithBilling(ctx, lipruntime.Options{ConfigPath: repoConfigPath(t)}, bad); err == nil {
		t.Fatal("typed-nil terminal must be rejected")
	}
	if counters.starts["worker-1"] != 0 {
		t.Fatal("owned resources must not start when binding validation fails")
	}
}

func TestBuildWithBillingUnwindsFailedOwnedStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	counters := &lifecycleCounters{}
	bad := validFacadeBinding(counters)
	bad.Lifecycle = billing.Lifecycle{Owned: []billing.OwnedResource{
		counters.owned("worker-a", nil),
		counters.owned("worker-b", errors.New("start failed")),
	}}
	if _, err := lipruntime.BuildWithBilling(ctx, lipruntime.Options{ConfigPath: repoConfigPath(t)}, bad); err == nil {
		t.Fatal("failing owned start must fail the build")
	}
	if counters.starts["worker-a"] != 1 || counters.starts["worker-b"] != 1 {
		t.Fatalf("starts = %v, want both attempted once", counters.starts)
	}
	if counters.closes["worker-a"] != 1 {
		t.Fatalf("worker-a closes = %d, want unwind exactly once", counters.closes["worker-a"])
	}
	if counters.closes["worker-b"] != 0 {
		t.Fatalf("worker-b closes = %d, want 0 (never started)", counters.closes["worker-b"])
	}
}

func TestBuildWithBillingCoexistsWithNonMoneyAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	counters := &lifecycleCounters{}
	rt, err := lipruntime.BuildWithBilling(ctx, lipruntime.Options{
		ConfigPath: repoConfigPath(t),
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "enterprise-req",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageRequestAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: allowRequestProvider{},
		}},
	}, validFacadeBinding(counters))
	if err != nil {
		t.Fatalf("BuildWithBilling with quota authority: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if !rt.Ready() {
		t.Fatal("expected ready runtime")
	}
}
