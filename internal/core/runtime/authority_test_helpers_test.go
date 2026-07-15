package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingledger "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/ledger"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// authorityAdmissionCountFunc is an adapter that lets a plain function satisfy the
// tokenaccounting preflight CountCall interface used by authority admission denial tests.
type authorityAdmissionCountFunc func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error)

func (f authorityAdmissionCountFunc) CountCall(ctx context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return f(ctx, in)
}

// authorityRuleSource is a minimal RuleSource stub used to build an authority
// service backed by a real in-memory store for end-to-end authority admission tests.
type authorityRuleSource struct {
	snapshot authorityapp.RuleSnapshot
}

func (s authorityRuleSource) Snapshot(context.Context) (authorityapp.RuleSnapshot, error) {
	return s.snapshot, nil
}

// authorityServiceRecorder wraps a real *authorityapp.Service to record admit
// inputs alongside the counts. Used by tests that want to assert on the actual
// rule-matching + reservation logic while still observing call counts.
type authorityServiceRecorder struct {
	admitMu      sync.Mutex
	admitInputsV []authorityapp.AdmissionInput
	svc          *authorityapp.Service

	admitCalls   atomic.Int64
	settleCalls  atomic.Int64
	releaseCalls atomic.Int64
}

func (s *authorityServiceRecorder) Admit(ctx context.Context, in authorityapp.AdmissionInput) (authorityapp.AdmissionResult, error) {
	s.admitCalls.Add(1)
	s.admitMu.Lock()
	s.admitInputsV = append(s.admitInputsV, in)
	s.admitMu.Unlock()
	return s.svc.Admit(ctx, in)
}

func (s *authorityServiceRecorder) Settle(ctx context.Context, in authorityapp.SettleInput) (authorityapp.SettleResult, error) {
	s.settleCalls.Add(1)
	return s.svc.Settle(ctx, in)
}

func (s *authorityServiceRecorder) Release(ctx context.Context, in authorityapp.ReleaseInput) (authorityapp.ReleaseResult, error) {
	s.releaseCalls.Add(1)
	return s.svc.Release(ctx, in)
}

func (s *authorityServiceRecorder) ApplyUsage(ctx context.Context, cmd authorityapp.ApplyUsageCommand) (authorityapp.ApplyUsageResult, error) {
	return s.svc.ApplyUsage(ctx, cmd)
}

func (s *authorityServiceRecorder) lastAdmit() authorityapp.AdmissionInput {
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	if len(s.admitInputsV) == 0 {
		return authorityapp.AdmissionInput{}
	}
	return s.admitInputsV[len(s.admitInputsV)-1]
}

func (s *authorityServiceRecorder) admitInputs() []authorityapp.AdmissionInput {
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	out := make([]authorityapp.AdmissionInput, len(s.admitInputsV))
	copy(out, s.admitInputsV)
	return out
}

// newRecordedAuthorityService builds an *authorityServiceRecorder backed by a real
// in-memory store + rule source. The single rule is the only enforceable rule for
// the test's lifetime; the store starts in Ready state.
func newRecordedAuthorityService(t *testing.T, rule authoritydomain.Rule) *authorityServiceRecorder {
	t.Helper()
	store := authoritystore.NewMemory(authoritystore.Config{
		StoreID: "authority-test",
		Backing: authoritydomain.BackingCapabilityAtomic,
		Readiness: authoritydomain.AuthorityStatus{
			State:  authoritydomain.AuthorityStateReady,
			Reason: authoritydomain.StatusReasonNone,
		},
	})
	svc := authorityapp.NewService(authorityRuleSource{
		snapshot: authorityapp.RuleSnapshot{
			Status: authoritydomain.AuthorityStatus{
				State:  authoritydomain.AuthorityStateReady,
				Reason: authoritydomain.StatusReasonNone,
			},
			Rules: []authoritydomain.Rule{rule},
		},
	}, store, nil, nil)
	return &authorityServiceRecorder{svc: svc}
}

// newAuthorityRuntimeTestExecutorWithStore builds an Executor wired to a b2bua
// MemoryStore, a single "backend-1" backend with the supplied open counter, and
// the supplied authority service. Returns the executor, store, backend counter,
// and the allocated A-leg ID.
func newAuthorityRuntimeTestExecutorWithStore(t *testing.T, authority UsageAuthorityService) (*Executor, *b2bua.MemoryStore, *authorityOpenCounter, string) {
	t.Helper()

	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	leg, err := store.CreateALeg(context.Background(), "authority-test")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}
	backend := &authorityOpenCounter{}
	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"backend-1": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
			}),
			EnforcesMaxOutputTokens: true,
			Open:                    backend.open,
		},
	}
	ex.UsageAuthority = authority
	return ex, store, backend, leg.ALegID
}

// authorityOpenParams builds the standard attemptOpenParams used by admission
// denial tests that exercise openPlannedCandidate directly.
func authorityOpenParams(t *testing.T, aLegID string, budget *attemptBudget) attemptOpenParams {
	t.Helper()
	return attemptOpenParams{
		ctx:     context.Background(),
		bus:     hooks.New(hooks.Config{}),
		traceID: "trace-1",
		aLegID:  aLegID,
		baseline: lipapi.Call{
			ID:    "request-1",
			Route: lipapi.RouteIntent{Selector: "backend-1:model-1"},
			Invocation: lipapi.Invocation{
				Operation:    lipapi.OperationOpenAIChatCompletions,
				DeliveryMode: lipapi.DeliveryModeStreaming,
			},
		},
		session:  &routing.SessionRoutingState{},
		excluded: map[string]struct{}{},
		rng:      routing.NewSeededRng(1),
		budget:   budget,
	}
}

// newAuthorityRuntimeTestExecutor is the lightweight variant that drops the b2bua
// store from the return tuple. Used by tests that only need the executor and
// backend counter (admission, settlement, release paths that don't need the
// b2bua B-leg state machine).
func newAuthorityRuntimeTestExecutor(t *testing.T, authority UsageAuthorityService) (*Executor, *authorityOpenCounter, string) {
	t.Helper()

	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	leg, err := store.CreateALeg(context.Background(), "authority-test")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}
	backend := &authorityOpenCounter{}
	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"backend-1": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
			}),
			EnforcesMaxOutputTokens: true,
			Open:                    backend.open,
		},
	}
	ex.UsageAuthority = authority
	return ex, backend, leg.ALegID
}

// openAuthorityCandidate runs a single authority-aware openPlannedCandidate call
// for the standard "backend-1:model-1" candidate.
func openAuthorityCandidate(t *testing.T, ex *Executor, aLegID string) (attemptOpenResult, error) {
	t.Helper()
	p := attemptOpenParams{
		ctx:     context.Background(),
		bus:     hooks.New(hooks.Config{}),
		traceID: "trace-1",
		aLegID:  aLegID,
		baseline: lipapi.Call{
			ID:    "request-1",
			Route: lipapi.RouteIntent{Selector: "backend-1:model-1"},
			Invocation: lipapi.Invocation{
				Operation:    lipapi.OperationOpenAIChatCompletions,
				DeliveryMode: lipapi.DeliveryModeStreaming,
			},
		},
	}
	return ex.openPlannedCandidate(p, authorityCandidate(), nil, "", false)
}

// authorityCandidate returns the standard "backend-1:model-1" attempt candidate
// used by most authority tests.
func authorityCandidate() routing.AttemptCandidate {
	return routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "backend-1", Model: "model-1"},
		Key:     "backend-1:model-1",
	}
}

// testAuthorityLifecycle builds an authorityLifecycle owner over the supplied
// executor's authority service, the supplied reservation state, and candidate. It is
// the test-side analog of newAuthorityLifecycle used at retryRecvStream construction
// sites, so leak/settlement/release tests can pre-stage reservation state on a stream
// literal without repeating the owner wiring. The candidate only feeds debug log
// attributes, so passing the stream's cand keeps log correlation consistent.
func testAuthorityLifecycle(ex *Executor, state attemptAuthorityState, cand routing.AttemptCandidate) authorityLifecycle {
	return ex.newAttemptAuthorityLifecycle(state, cand)
}

// authorityInputAmount builds the standard InputTokens amount used to reserve
// against the "backend-1:model-1" rule in most authority tests.
func authorityInputAmount(v int64) authoritydomain.Amount {
	return authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: v}
}

// testAuthorityAdmissionInput builds a canonical admission input used by
// settlement / release / replacement tests that pre-stage the admit result
// and only exercise downstream authority behavior.
func testAuthorityAdmissionInput(amount int64) authorityapp.AdmissionInput {
	reqID := "request-1"
	legID := "a-leg-1"
	blegID := "bleg-1"
	key := authoritydomain.ReservationKey{
		LogicalRequestID: reqID,
		ALegID:           legID,
		BLegID:           blegID,
		AttemptID:        blegID,
		RuleID:           "rule-1",
		Sequence:         1,
	}
	return authorityapp.AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID:    "trace-1",
			RequestID:  reqID,
			ALegID:     legID,
			BLegID:     blegID,
			AttemptSeq: 1,
			BackendID:  "backend-1",
			Model:      "model-1",
		},
		Scope:          scope.PrincipalScopeView{},
		Dimensions:     authoritydomain.Dimensions{},
		Request:        authorityInputAmount(amount),
		Spend:          authoritydomain.Amount{Unit: authoritydomain.AmountUnitMoneyNano, Value: amount, Currency: "USD"},
		Authority:      authoritydomain.AuthorityLevelEstimated,
		ReservationKey: key,
		EstimateOnly:   false,
	}
}

// authorityOpenCounter counts backend Open invocations and optionally routes
// through a caller-supplied openFn for stream-content overrides.
type authorityOpenCounter struct {
	openCalls atomic.Int64
	openFn    func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error)
}

func (c *authorityOpenCounter) open(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
	c.openCalls.Add(1)
	if c.openFn != nil {
		return c.openFn(ctx, call, cand)
	}
	return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
}

// recordingAuthorityService is a fully programmable UsageAuthorityService used
// to script admit/settle/release outcomes and to inspect the most recent
// admit/settle/release inputs. It implements all the SDK's authority ports.
type recordingAuthorityService struct {
	admitMu        sync.Mutex
	admitInputsV   []authorityapp.AdmissionInput
	settleMu       sync.Mutex
	settleInputsV  []authorityapp.SettleInput
	releaseMu      sync.Mutex
	releaseInputsV []authorityapp.ReleaseInput
	admitCalls     atomic.Int64
	settleCalls    atomic.Int64
	releaseCalls   atomic.Int64

	admitResult authorityapp.AdmissionResult
	admitErr    error
	settleErr   error
	releaseErr  error

	lastAdmitInput   atomic.Value
	lastSettleInput  atomic.Value
	lastReleaseInput atomic.Value

	status controlplane.AccountingAuthorityStatus
}

func (s *recordingAuthorityService) Admit(_ context.Context, in authorityapp.AdmissionInput) (authorityapp.AdmissionResult, error) {
	s.admitCalls.Add(1)
	s.admitMu.Lock()
	s.admitInputsV = append(s.admitInputsV, in)
	s.admitMu.Unlock()
	s.lastAdmitInput.Store(in)
	return s.admitResult, s.admitErr
}

func (s *recordingAuthorityService) Settle(_ context.Context, in authorityapp.SettleInput) (authorityapp.SettleResult, error) {
	s.settleCalls.Add(1)
	s.settleMu.Lock()
	s.settleInputsV = append(s.settleInputsV, in)
	s.settleMu.Unlock()
	s.lastSettleInput.Store(in)
	if s.settleErr != nil {
		return authorityapp.SettleResult{}, s.settleErr
	}
	return authorityapp.SettleResult{Applied: true}, nil
}

func (s *recordingAuthorityService) Release(_ context.Context, in authorityapp.ReleaseInput) (authorityapp.ReleaseResult, error) {
	s.releaseCalls.Add(1)
	s.releaseMu.Lock()
	s.releaseInputsV = append(s.releaseInputsV, in)
	s.releaseMu.Unlock()
	s.lastReleaseInput.Store(in)
	if s.releaseErr != nil {
		return authorityapp.ReleaseResult{}, s.releaseErr
	}
	return authorityapp.ReleaseResult{Applied: true}, nil
}

func (s *recordingAuthorityService) ApplyUsage(_ context.Context, cmd authorityapp.ApplyUsageCommand) (authorityapp.ApplyUsageResult, error) {
	return authorityapp.ApplyUsageResult{Applied: len(cmd.RuleIDs) > 0, RuleIDs: append([]string(nil), cmd.RuleIDs...)}, nil
}

func (s *recordingAuthorityService) Status(context.Context) (controlplane.AccountingAuthorityStatus, error) {
	if s.status.State == "" {
		return controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady}, nil
	}
	return s.status, nil
}

func (s *recordingAuthorityService) LimitStatus(context.Context, controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error) {
	return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, nil
}

func (s *recordingAuthorityService) Decisions(context.Context, controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error) {
	return controlplane.Page[controlplane.AccountingDecisionRow]{}, nil
}

func (s *recordingAuthorityService) lastAdmit() authorityapp.AdmissionInput {
	if v := s.lastAdmitInput.Load(); v != nil {
		if in, ok := v.(authorityapp.AdmissionInput); ok {
			return in
		}
	}
	return authorityapp.AdmissionInput{}
}

func (s *recordingAuthorityService) admitInputs() []authorityapp.AdmissionInput {
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	out := make([]authorityapp.AdmissionInput, len(s.admitInputsV))
	copy(out, s.admitInputsV)
	return out
}

func (s *recordingAuthorityService) lastSettle() authorityapp.SettleInput {
	if v := s.lastSettleInput.Load(); v != nil {
		if in, ok := v.(authorityapp.SettleInput); ok {
			return in
		}
	}
	return authorityapp.SettleInput{}
}

func (s *recordingAuthorityService) settleInputs() []authorityapp.SettleInput {
	s.settleMu.Lock()
	defer s.settleMu.Unlock()
	out := make([]authorityapp.SettleInput, len(s.settleInputsV))
	copy(out, s.settleInputsV)
	return out
}

func (s *recordingAuthorityService) lastRelease() authorityapp.ReleaseInput {
	if v := s.lastReleaseInput.Load(); v != nil {
		if in, ok := v.(authorityapp.ReleaseInput); ok {
			return in
		}
	}
	return authorityapp.ReleaseInput{}
}

func (s *recordingAuthorityService) releaseInputs() []authorityapp.ReleaseInput {
	s.releaseMu.Lock()
	defer s.releaseMu.Unlock()
	out := make([]authorityapp.ReleaseInput, len(s.releaseInputsV))
	copy(out, s.releaseInputsV)
	return out
}

// stubStreamCounter is a deterministic stream-usage counter used by settlement
// tests that need to assert on reconstructed usage.
type stubStreamCounter struct {
	call   accountingapp.CountResult
	output accountingapp.CountResult
}

func (c *stubStreamCounter) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return c.call, nil
}

func (c *stubStreamCounter) CountOutput(context.Context, accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return c.output, nil
}

// failingLedger is a ledger stub that always returns the supplied error,
// used by ledger-failure settlement tests.
type failingLedger struct{ err error }

func (l failingLedger) Record(context.Context, accountingledger.Record) error { return l.err }

// estimateThenFailAuthority is a UsageAuthorityService stub that succeeds for the
// estimate-only precheck admit and fails the first real (non-estimate) admit with
// the configured error. It models a strict authority store returning
// ErrReservationConflict when the live reservation window is full, so the precheck
// passes while the authoritative admit fails after the budget slot and B-leg seq
// are already allocated.
type estimateThenFailAuthority struct {
	estimateResult authorityapp.AdmissionResult
	realErr        error
	admitCalls     atomic.Int64
	realFailed     atomic.Bool
}

func (s *estimateThenFailAuthority) Admit(_ context.Context, in authorityapp.AdmissionInput) (authorityapp.AdmissionResult, error) {
	s.admitCalls.Add(1)
	if in.EstimateOnly {
		return s.estimateResult, nil
	}
	if !s.realFailed.Swap(true) {
		return authorityapp.AdmissionResult{}, s.realErr
	}
	return s.estimateResult, nil
}

func (s *estimateThenFailAuthority) Settle(_ context.Context, _ authorityapp.SettleInput) (authorityapp.SettleResult, error) {
	return authorityapp.SettleResult{Applied: true}, nil
}

func (s *estimateThenFailAuthority) Release(_ context.Context, _ authorityapp.ReleaseInput) (authorityapp.ReleaseResult, error) {
	return authorityapp.ReleaseResult{Applied: true}, nil
}

func (s *estimateThenFailAuthority) ApplyUsage(_ context.Context, cmd authorityapp.ApplyUsageCommand) (authorityapp.ApplyUsageResult, error) {
	return authorityapp.ApplyUsageResult{Applied: len(cmd.RuleIDs) > 0, RuleIDs: append([]string(nil), cmd.RuleIDs...)}, nil
}
