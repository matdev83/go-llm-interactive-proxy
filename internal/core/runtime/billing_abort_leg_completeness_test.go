package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
)

type abortJoinCapture struct {
	mu    sync.Mutex
	calls []billing.CallUsageRecord
	legs  []billing.CallLegUsageRecord
}

func (c *abortJoinCapture) appendCall(_ context.Context, record billing.CallUsageRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, record)
	return nil
}

func (c *abortJoinCapture) appendLeg(_ context.Context, record billing.CallLegUsageRecord) error {
	sealed, err := record.Seal()
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.legs = append(c.legs, sealed)
	return nil
}

func (c *abortJoinCapture) snapshot() ([]billing.CallUsageRecord, []billing.CallLegUsageRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	calls := append([]billing.CallUsageRecord(nil), c.calls...)
	legs := append([]billing.CallLegUsageRecord(nil), c.legs...)
	return calls, legs
}

type failClosedStreamObserverFactory struct{}

func (failClosedStreamObserverFactory) ID() string                        { return "fail-closed-assemble" }
func (failClosedStreamObserverFactory) Order() int                        { return 0 }
func (failClosedStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (failClosedStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, errors.New("assemble observer open boom")
}

func wireAbortBilling(ex *Executor, capture *abortJoinCapture) {
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingAuthoritative = true
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billing.CallExposure, error) {
		return billing.CallExposure{
			AccountID: "acct", CallID: in.CallID, Status: billing.ExposureOpen,
			PricingRef:      billing.VersionRef{ID: "pricing:test", Version: "1"},
			ChargePolicyRef: billing.VersionRef{ID: "policy:test", Version: "1"},
		}, nil
	})
	ex.CallUsageAppender = billing.CallUsageAppenderFunc(capture.appendCall)
	ex.CallLegUsageAppender = billing.CallLegUsageAppenderFunc(capture.appendLeg)
}

func assertJoinableAbort(t *testing.T, calls []billing.CallUsageRecord, legs []billing.CallLegUsageRecord) {
	t.Helper()
	if len(calls) != 1 {
		t.Fatalf("call-closure appends = %d, want 1", len(calls))
	}
	if len(calls[0].ExpectedBLegIDs) == 0 {
		t.Fatal("expected at least one frozen B-leg after Open")
	}
	if len(legs) == 0 {
		t.Fatal("expected independent terminal leg rows")
	}
	seen := map[string]bool{}
	for _, leg := range legs {
		seen[strings.TrimSpace(leg.BLegID)] = true
		if leg.Outcome != billing.LegOutcomeFailed && leg.Outcome != billing.LegOutcomeNeverStarted {
			t.Fatalf("leg %q outcome = %s, want Failed or NeverStarted", leg.BLegID, leg.Outcome)
		}
	}
	for _, id := range calls[0].ExpectedBLegIDs {
		if !seen[id] {
			t.Fatalf("expected B-leg %q missing from terminal leg rows %#v", id, legs)
		}
	}
	if _, err := billing.JoinCompleteCall(calls[0], legs); err != nil {
		t.Fatalf("abort path must remain joinable: %v", err)
	}
}

func TestAssembleAbortAfterOpenAppendsTerminalLegForJoin(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	capture := &abortJoinCapture{}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	wireAbortBilling(ex, capture)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		StreamObserverFactories: []response.StreamObserverFactory{failClosedStreamObserverFactory{}},
	})
	ex.Backends = map[string]execbackend.Backend{
		"ok": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &lipapi.Call{
		Session:  lipapi.SessionRef{AuthoritativeSessionID: "sess-assemble-abort", ContinuityKey: "sess-assemble-abort"},
		Route:    lipapi.RouteIntent{Selector: "ok:m"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected assemble failure from fail-closed stream observer")
	}
	calls, legs := capture.snapshot()
	assertJoinableAbort(t, calls, legs)
	for _, leg := range legs {
		if leg.Outcome != billing.LegOutcomeFailed {
			t.Fatalf("assemble-after-open leg outcome = %s, want Failed", leg.Outcome)
		}
	}
}

func TestOpenInitialRegisterBLegFailureAppendsTerminalLegForJoin(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	capture := &abortJoinCapture{}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	wireAbortBilling(ex, capture)
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	ex.ALegLifecycle = coord
	ex.Backends = map[string]execbackend.Backend{
		"ok": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				aLegID := strings.TrimSpace(call.Session.ALegID)
				if aLegID == "" {
					return nil, errors.New("missing a-leg id on open")
				}
				_ = coord.CancelALeg(ctx, aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit})
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			},
		},
	}
	call := &lipapi.Call{
		Session:  lipapi.SessionRef{AuthoritativeSessionID: "sess-register-abort", ContinuityKey: "sess-register-abort"},
		Route:    lipapi.RouteIntent{Selector: "ok:m"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected RegisterBLeg failure after Open")
	}
	calls, legs := capture.snapshot()
	assertJoinableAbort(t, calls, legs)
}

func TestParallelRegisterBLegFailureAppendsTerminalLegForJoin(t *testing.T) {
	capture := &abortJoinCapture{}
	auth := reservedAuthorityRecorder("res-billing-l2")
	ex, _, backend, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)
	wireAbortBilling(ex, capture)
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)
	backend.openFn = func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		_ = coord.CancelALeg(ctx, aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit})
		return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
	}

	p := authorityOpenParams(t, aLegID, &attemptBudget{max: 10})
	p.aScope = aScope
	p.billingCallID = callID
	p.billingCallState = newBillingCallState(callID)

	raceErr := runRaceInGoroutine(t, 5*time.Second, func() error {
		_, err := ex.tryOpenParallelGroup(context.Background(), p, []routing.AttemptCandidate{authorityCandidate()}, nil, "", false)
		return err
	})
	if raceErr == nil {
		t.Fatal("expected parallel race error from RegisterBLeg failure")
	}

	_, legs := capture.snapshot()
	if len(legs) == 0 {
		t.Fatal("RegisterBLeg failure after Open must append independent terminal leg")
	}
	if legs[0].Outcome != billing.LegOutcomeFailed {
		t.Fatalf("parallel RegisterBLeg failure outcome = %s, want Failed", legs[0].Outcome)
	}
	if legs[0].CallID != callID {
		t.Fatalf("leg CallID = %s, want %s", legs[0].CallID, callID)
	}

	frozen := p.billingCallState.freezeAllocatedBLegs()
	if len(frozen) == 0 {
		t.Fatal("expected allocated B-leg to remain frozen after RegisterBLeg failure")
	}
	record := billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             callID,
		AccountID:          "acct",
		ALegID:             aLegID,
		Outcome:            billing.TurnOutcomeFailed,
		CustomerPricingRef: billing.VersionRef{ID: "pricing:test", Version: "1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy:test", Version: "1"},
		ExpectedBLegIDs:    frozen,
		StartedAt:          time.Unix(1, 0).UTC(),
		FinishedAt:         time.Unix(2, 0).UTC(),
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := billing.JoinCompleteCall(sealed, legs); err != nil {
		t.Fatalf("parallel RegisterBLeg failure must remain joinable: %v", err)
	}
}

type boomRequestPartHook struct{}

func (boomRequestPartHook) ID() string { return "boom-request-part" }
func (boomRequestPartHook) Order() int { return 0 }
func (boomRequestPartHook) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}
func (boomRequestPartHook) HandleRequestParts(context.Context, *lipapi.Call, sdkhooks.PartMeta) error {
	return errors.New("request part hook boom after NextBLeg")
}

type toolsExcludeRequestPartHook struct{}

func (toolsExcludeRequestPartHook) ID() string { return "tools-exclude-part" }
func (toolsExcludeRequestPartHook) Order() int { return 0 }
func (toolsExcludeRequestPartHook) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}
func (toolsExcludeRequestPartHook) HandleRequestParts(_ context.Context, call *lipapi.Call, _ sdkhooks.PartMeta) error {
	if call == nil {
		return nil
	}
	call.Tools = append(call.Tools, lipapi.ToolDef{Name: "need-tools", Description: "x"})
	return nil
}

// assertClosureJoinableOrEmpty requires that any emitted call-closure ExpectedBLegIDs
// set is fully backed by durable terminal leg rows (no ghost freeze membership).
func assertClosureJoinableOrEmpty(t *testing.T, calls []billing.CallUsageRecord, legs []billing.CallLegUsageRecord) {
	t.Helper()
	if len(calls) == 0 {
		return
	}
	if len(calls) != 1 {
		t.Fatalf("call-closure appends = %d, want 0 or 1", len(calls))
	}
	seen := map[string]bool{}
	for _, leg := range legs {
		seen[strings.TrimSpace(leg.BLegID)] = true
	}
	for _, id := range calls[0].ExpectedBLegIDs {
		if !seen[id] {
			t.Fatalf("ghost ExpectedBLegID %q has no terminal leg row; legs=%#v expected=%#v", id, legs, calls[0].ExpectedBLegIDs)
		}
	}
	if _, err := billing.JoinCompleteCall(calls[0], legs); err != nil {
		t.Fatalf("emitted call-closure must remain joinable: %v", err)
	}
}

func TestRequestPartHookFailureAfterNextBLegDoesNotFreezeGhostBLeg(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	capture := &abortJoinCapture{}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{boomRequestPartHook{}}})
	ex.Rand = routing.NewSeededRng(1)
	wireAbortBilling(ex, capture)
	ex.Backends = map[string]execbackend.Backend{
		"ok": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				t.Fatal("Open must not run after request-part hook failure")
				return nil, nil
			},
		},
	}
	call := &lipapi.Call{
		Session:  lipapi.SessionRef{AuthoritativeSessionID: "sess-hook-abort", ContinuityKey: "sess-hook-abort"},
		Route:    lipapi.RouteIntent{Selector: "ok:m"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected request-part hook failure")
	}
	calls, legs := capture.snapshot()
	assertClosureJoinableOrEmpty(t, calls, legs)
}

func TestPostHookExcludeAfterNextBLegDoesNotFreezeExcludedCandidate(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	capture := &abortJoinCapture{}
	ex := TestExecutor()
	ex.Store = st
	ex.MaxAttempts = 4
	ex.Rand = routing.NewSeededRng(3)
	ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{toolsExcludeRequestPartHook{}}})
	wireAbortBilling(ex, capture)
	var openedA int
	ex.Backends = map[string]execbackend.Backend{
		"a": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				openedA++
				t.Fatal("tools-incapable candidate must be excluded before Open")
				return nil, nil
			},
		},
		"b": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &lipapi.Call{
		ID:       "exclude-ghost",
		Session:  lipapi.SessionRef{AuthoritativeSessionID: "sess-exclude-ghost", ContinuityKey: "sess-exclude-ghost"},
		Route:    lipapi.RouteIntent{Selector: "[first]a:m^b:m"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIChatCompletions,
			DeliveryMode: lipapi.DeliveryModeStreaming,
		},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if openedA != 0 {
		t.Fatalf("excluded candidate opened %d times", openedA)
	}
	calls, legs := capture.snapshot()
	assertClosureJoinableOrEmpty(t, calls, legs)
}

func TestAuthorityAdmitFailureAfterNextBLegDoesNotFreezeGhostBLeg(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	capture := &abortJoinCapture{}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	wireAbortBilling(ex, capture)
	auth := &estimateThenFailAuthority{
		estimateResult: authorityapp.AdmissionResult{Allowed: true, Reserved: true, ReservationID: "res-est"},
		realErr:        authorityapp.ErrReservationConflict,
	}
	ex.UsageAuthority = auth
	ex.Backends = map[string]execbackend.Backend{
		"ok": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				t.Fatal("Open must not run after authoritative admit failure")
				return nil, nil
			},
		},
	}
	call := &lipapi.Call{
		Session:  lipapi.SessionRef{AuthoritativeSessionID: "sess-admit-abort", ContinuityKey: "sess-admit-abort"},
		Route:    lipapi.RouteIntent{Selector: "ok:m"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected authoritative admit failure")
	}
	calls, legs := capture.snapshot()
	assertClosureJoinableOrEmpty(t, calls, legs)
}
