package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	secureapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type emitTestObserver struct {
	events   []lipapi.Event
	outcomes []response.StreamOutcome
	finishN  atomic.Int64
	mu       sync.Mutex
}

func (o *emitTestObserver) Observe(_ context.Context, ev lipapi.Event) error {
	o.mu.Lock()
	o.events = append(o.events, ev)
	o.mu.Unlock()
	return nil
}

func (o *emitTestObserver) Finish(_ context.Context, outcome response.StreamOutcome) error {
	o.finishN.Add(1)
	o.mu.Lock()
	o.outcomes = append(o.outcomes, outcome)
	o.mu.Unlock()
	return nil
}

type emitTestObserverFactory struct {
	observers []*emitTestObserver
	obsMu     sync.Mutex
}

func (f *emitTestObserverFactory) ID() string                        { return "emit-test-obs" }
func (f *emitTestObserverFactory) Order() int                        { return 0 }
func (f *emitTestObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (f *emitTestObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	obs := &emitTestObserver{}
	f.obsMu.Lock()
	f.observers = append(f.observers, obs)
	f.obsMu.Unlock()
	return obs, nil
}

func (f *emitTestObserverFactory) snapshot() []*emitTestObserver {
	f.obsMu.Lock()
	defer f.obsMu.Unlock()
	out := make([]*emitTestObserver, len(f.observers))
	copy(out, f.observers)
	return out
}

type equalReplaceGate struct{}

func (equalReplaceGate) ID() string                        { return "equal-replace" }
func (equalReplaceGate) Order() int                        { return 0 }
func (equalReplaceGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (equalReplaceGate) Handle(_ context.Context, _ completion.Meta, buf completion.Buffered, _ completion.Services) (completion.Outcome, error) {
	events := buf.Events()
	return completion.ReplaceOutcome(append([]lipapi.Event(nil), events...)), nil
}

type failingSecureRecorderEmit struct{ err error }

func (f failingSecureRecorderEmit) RecordClientTurnAfterGate(context.Context, secureapp.ClientTurnRecordInput) error {
	return nil
}

func (f failingSecureRecorderEmit) RecordPostHookStreamEvent(context.Context, secureapp.StreamEventRecordInput) error {
	return f.err
}

func setupEmitObserverStream(t *testing.T, auth *recordingAuthorityService, factory *emitTestObserverFactory, gates []completion.Gate) (*Executor, *retryRecvStream) {
	t.Helper()
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{
		call:   accountingapp.CountResult{InputTokens: 7, TotalTokens: 7},
		output: accountingapp.CountResult{OutputTokens: 3, TotalTokens: 10},
	}, accountingstream.Config{})
	bus := hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		CompletionGates:         gates,
		StreamObserverFactories: []response.StreamObserverFactory{factory},
	})
	rs := &retryRecvStream{
		executor: ex,
		bus:      bus,
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{ID: "emit-obs", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-emit",
			aLegID:   aLegID,
		}),
		bleg:       b2bua.BLegRecord{BLegID: "b-leg-emit", Seq: 1},
		cand:       authorityCandidate(),
		authority:  testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()),
		accounting: newAttemptAccountingTracker(time.Unix(1, 0)),
	}
	if err := rs.openFinalStreamObservation(context.Background()); err != nil {
		t.Fatalf("open observer: %v", err)
	}
	return ex, rs
}

func TestEmitClientFacingObserved_recoverDrainFinishObservedAndReleased(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-emit-drain",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		settleErr: errors.New("settle boom"),
		status:    controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	factory := &emitTestObserverFactory{}
	_, rs := setupEmitObserverStream(t, auth, factory, []completion.Gate{gatedLeakPassGate{}})

	first, cont, err := rs.handleRecvSuccess(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
	if err != nil {
		t.Fatalf("handleRecvSuccess: %v", err)
	}
	if cont || first.Kind != lipapi.EventUsageDelta {
		t.Fatalf("first=%#v cont=%v want usage_delta", first, cont)
	}

	finish, err := rs.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if finish.Kind != lipapi.EventResponseFinished {
		t.Fatalf("finish kind=%q", finish.Kind)
	}

	observers := factory.snapshot()
	if len(observers) == 0 {
		t.Fatal("expected observer")
	}
	obs := observers[len(observers)-1]
	obs.mu.Lock()
	sawFinished := false
	sawUsage := false
	for _, ev := range obs.events {
		switch ev.Kind {
		case lipapi.EventResponseFinished:
			sawFinished = true
		case lipapi.EventUsageDelta:
			sawUsage = true
		}
	}
	outcomes := append([]response.StreamOutcome(nil), obs.outcomes...)
	obs.mu.Unlock()
	if !sawUsage {
		t.Fatal("synthesized usage must be Observed via emitClientFacingObserved")
	}
	if !sawFinished {
		t.Fatal("recoverDrain pop must Observe response_finished")
	}
	if obs.finishN.Load() != 1 || len(outcomes) != 1 || outcomes[0] != response.OutcomeSuccessReleased {
		t.Fatalf("want Finish(success_released) once; finishN=%d outcomes=%#v", obs.finishN.Load(), outcomes)
	}
}

func TestEmitClientFacingObserved_mandatoryBeforeEmitFinishFailed(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-emit-fail",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	factory := &emitTestObserverFactory{}
	ex, rs := setupEmitObserverStream(t, auth, factory, nil)
	ex.StreamUsage = nil
	recErr := errors.New("recorder boom")
	ex.SecureSessionRecorder = failingSecureRecorderEmit{err: recErr}
	ex.SecureSessionRecordingMandatory = true
	rs = withTestRecvFacts(rs, func(f recvTurnFacts) recvTurnFacts {
		f.secureTurnOK = true
		return f
	})

	_, cont, err := rs.handleRecvSuccess(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
	if err == nil {
		t.Fatal("want mandatory recorder error")
	}
	if cont {
		t.Fatal("cont=false")
	}
	observers := factory.snapshot()
	if len(observers) == 0 {
		t.Fatal("expected observer")
	}
	obs := observers[0]
	obs.mu.Lock()
	outcomes := append([]response.StreamOutcome(nil), obs.outcomes...)
	obs.mu.Unlock()
	if len(outcomes) != 1 || outcomes[0] != response.OutcomeFailed {
		t.Fatalf("mandatory beforeEmit failure must Finish(failed); outcomes=%#v", outcomes)
	}
}

func TestEmitClientFacingObserved_RemembersAfterSuccessfulRecording(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-remember-ok",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	factory := &emitTestObserverFactory{}
	_, rs := setupEmitObserverStream(t, auth, factory, nil)
	rs.customer = newCustomerEvidenceAccumulator()

	out, err := rs.emitClientFacingObserved(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"}, sdkhooks.PartMeta{})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if out.Delta != "hello" {
		t.Fatalf("delta=%q", out.Delta)
	}
	text, _, _, n := rs.customer.Snapshot()
	if text != "hello" || n != 1 {
		t.Fatalf("customer evidence text=%q n=%d; remember must run after successful recording", text, n)
	}
	observers := factory.snapshot()
	if len(observers) == 0 || len(observers[0].events) != 1 {
		t.Fatalf("final-stream observation must still see the event; observers=%d", len(observers))
	}
}

func TestEmitClientFacingObserved_MandatoryFailureDoesNotRemember(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-remember-fail",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	factory := &emitTestObserverFactory{}
	ex, rs := setupEmitObserverStream(t, auth, factory, nil)
	rs.customer = newCustomerEvidenceAccumulator()
	recErr := errors.New("recorder boom")
	ex.SecureSessionRecorder = failingSecureRecorderEmit{err: recErr}
	ex.SecureSessionRecordingMandatory = true
	rs = withTestRecvFacts(rs, func(f recvTurnFacts) recvTurnFacts {
		f.secureTurnOK = true
		return f
	})

	_, err := rs.emitClientFacingObserved(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "undelivered"}, sdkhooks.PartMeta{})
	if !errors.Is(err, recErr) {
		t.Fatalf("err=%v want recorder boom", err)
	}
	text, _, _, n := rs.customer.Snapshot()
	if text != "" || n != 0 {
		t.Fatalf("undelivered output must not enter customer evidence; text=%q n=%d", text, n)
	}
}

func TestEmitClientFacingObserved_equalContentGateReplaceCyclesLifecycle(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-equal-gate",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	factory := &emitTestObserverFactory{}
	_, rs := setupEmitObserverStream(t, auth, factory, []completion.Gate{equalReplaceGate{}})

	first, cont, err := rs.handleRecvSuccess(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
	if err != nil || cont {
		t.Fatalf("finish: err=%v cont=%v", err, cont)
	}
	if first.Kind != lipapi.EventUsageDelta {
		t.Fatalf("with StreamUsage, first client event should be synthesized usage; got %#v", first)
	}
	finish, err := rs.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv finish: %v", err)
	}
	if finish.Kind != lipapi.EventResponseFinished {
		t.Fatalf("finish kind=%q", finish.Kind)
	}

	observers := factory.snapshot()
	if len(observers) < 2 {
		t.Fatalf("equal-content replace must cycle lifecycle; opens=%d", len(observers))
	}
	if observers[0].finishN.Load() != 1 {
		t.Fatalf("first observer Finish once; got %d", observers[0].finishN.Load())
	}
	observers[0].mu.Lock()
	firstOutcomes := append([]response.StreamOutcome(nil), observers[0].outcomes...)
	observers[0].mu.Unlock()
	if len(firstOutcomes) != 1 || firstOutcomes[0] != response.OutcomeGateReplaced {
		t.Fatalf("first lifecycle want gate_replaced; got %#v", firstOutcomes)
	}
	last := observers[len(observers)-1]
	last.mu.Lock()
	sawFinished := false
	sawUsage := false
	for _, ev := range last.events {
		switch ev.Kind {
		case lipapi.EventResponseFinished:
			sawFinished = true
		case lipapi.EventUsageDelta:
			sawUsage = true
		}
	}
	lastOutcomes := append([]response.StreamOutcome(nil), last.outcomes...)
	last.mu.Unlock()
	if !sawUsage {
		t.Fatal("replacement lifecycle must observe synthesized usage")
	}
	if !sawFinished {
		t.Fatal("replacement lifecycle must observe response_finished")
	}
	if len(lastOutcomes) != 1 || lastOutcomes[0] != response.OutcomeSuccessReleased {
		t.Fatalf("replacement lifecycle want success_released; got %#v", lastOutcomes)
	}
}

func TestStreamObserverMeta_clonesScope(t *testing.T) {
	t.Parallel()
	orig := scope.PrincipalScopeView{
		PrincipalID: scope.Known("orig-principal"),
		Roles:       []string{"reader"},
	}
	rs := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID: "t1",
			aLegID:  "a1",
		}),
		bleg: b2bua.BLegRecord{BLegID: "b1", Seq: 1},
		cand: authorityCandidate(),
	}
	ctx := execctx.WithViews(context.Background(), execctx.Views{Scope: orig.Clone()})
	meta := rs.streamObserverMeta(ctx)
	if meta.Scope.PrincipalID.String() != "orig-principal" {
		t.Fatalf("meta scope=%q", meta.Scope.PrincipalID)
	}
	meta.Scope.PrincipalID = scope.Known("mutated")
	if len(meta.Scope.Roles) > 0 {
		meta.Scope.Roles[0] = "mutated-role"
	}
	if orig.PrincipalID.String() != "orig-principal" {
		t.Fatalf("source scope mutated: %#v", orig)
	}
	if orig.Roles[0] != "reader" {
		t.Fatalf("source roles mutated: %#v", orig.Roles)
	}
}

type orderedEmitObserver struct {
	mu                 sync.Mutex
	ops                []string
	observeAfterFinish bool
	finished           bool
	observeEntered     chan struct{}
	releaseObserve     chan struct{}
	blockOnce          sync.Once
}

func (o *orderedEmitObserver) Observe(_ context.Context, ev lipapi.Event) error {
	if o.observeEntered != nil {
		o.blockOnce.Do(func() { close(o.observeEntered) })
	}
	if o.releaseObserve != nil {
		<-o.releaseObserve
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		o.observeAfterFinish = true
	}
	o.ops = append(o.ops, "observe:"+string(ev.Kind))
	return nil
}

func (o *orderedEmitObserver) Finish(_ context.Context, outcome response.StreamOutcome) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finished = true
	o.ops = append(o.ops, "finish:"+string(outcome))
	return nil
}

type orderedEmitObserverFactory struct {
	obs   *orderedEmitObserver
	mode  sdkhooks.FailureMode
	obsMu sync.Mutex
	n     int
}

func (f *orderedEmitObserverFactory) ID() string                        { return "ordered-emit-obs" }
func (f *orderedEmitObserverFactory) Order() int                        { return 0 }
func (f *orderedEmitObserverFactory) FailureMode() sdkhooks.FailureMode { return f.mode }
func (f *orderedEmitObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	f.obsMu.Lock()
	defer f.obsMu.Unlock()
	f.n++
	return f.obs, nil
}

type nthOpenFailFactory struct {
	failAfter int64
	mode      sdkhooks.FailureMode
	opens     atomic.Int64
	observers []*emitTestObserver
	obsMu     sync.Mutex
}

func (f *nthOpenFailFactory) ID() string                        { return "nth-open-fail" }
func (f *nthOpenFailFactory) Order() int                        { return 0 }
func (f *nthOpenFailFactory) FailureMode() sdkhooks.FailureMode { return f.mode }
func (f *nthOpenFailFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	n := f.opens.Add(1)
	if n > f.failAfter {
		return nil, errors.New("open fail-closed")
	}
	obs := &emitTestObserver{}
	f.obsMu.Lock()
	f.observers = append(f.observers, obs)
	f.obsMu.Unlock()
	return obs, nil
}

func (f *nthOpenFailFactory) snapshot() []*emitTestObserver {
	f.obsMu.Lock()
	defer f.obsMu.Unlock()
	out := make([]*emitTestObserver, len(f.observers))
	copy(out, f.observers)
	return out
}

type failClosedObserveFactory struct {
	obs   *emitTestObserver
	err   error
	opens atomic.Int64
}

func (f *failClosedObserveFactory) ID() string                        { return "fail-closed-observe" }
func (f *failClosedObserveFactory) Order() int                        { return 0 }
func (f *failClosedObserveFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (f *failClosedObserveFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	f.opens.Add(1)
	return failClosedObserve{inner: f.obs, err: f.err}, nil
}

type failClosedObserve struct {
	inner *emitTestObserver
	err   error
}

func (o failClosedObserve) Observe(ctx context.Context, ev lipapi.Event) error {
	if o.inner != nil {
		_ = o.inner.Observe(ctx, ev)
	}
	return o.err
}

func (o failClosedObserve) Finish(ctx context.Context, outcome response.StreamOutcome) error {
	if o.inner != nil {
		return o.inner.Finish(ctx, outcome)
	}
	return nil
}

func TestEmitClientFacingObserved_concurrentCloseRecv(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-race",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	obs := &orderedEmitObserver{
		observeEntered: make(chan struct{}),
		releaseObserve: make(chan struct{}),
	}
	factory := &orderedEmitObserverFactory{obs: obs, mode: sdkhooks.FailOpen}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	bus := hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		StreamObserverFactories: []response.StreamObserverFactory{factory},
	})
	rs := &retryRecvStream{
		executor: ex,
		bus:      bus,
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{ID: "emit-race", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-race",
			aLegID:   aLegID,
		}),
		bleg:       b2bua.BLegRecord{BLegID: "b-leg-race", Seq: 1},
		cand:       authorityCandidate(),
		authority:  testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()),
		accounting: newAttemptAccountingTracker(time.Unix(1, 0)),
	}
	if err := rs.openFinalStreamObservation(context.Background()); err != nil {
		t.Fatalf("open observer: %v", err)
	}
	// Non-terminal drain event so Recv Observes before markFinished; Close must serialize Finish.
	rs.recoverDrain = []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "race"}}

	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = rs.Recv(ctx)
	})
	select {
	case <-obs.observeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Observe did not start")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- rs.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return")
	}
	time.Sleep(20 * time.Millisecond)
	obs.mu.Lock()
	if obs.finished {
		obs.mu.Unlock()
		t.Fatal("Finish callback must not run until Observe completes")
	}
	obs.mu.Unlock()
	close(obs.releaseObserve)
	wg.Wait()
	deadline := time.After(2 * time.Second)
	for {
		obs.mu.Lock()
		doneFinish := obs.finished
		ops := append([]string(nil), obs.ops...)
		after := obs.observeAfterFinish
		obs.mu.Unlock()
		if doneFinish {
			if after {
				t.Fatalf("Observe must never run after Finish; ops=%v", ops)
			}
			if len(ops) < 2 || ops[0] != "observe:text_delta" {
				t.Fatalf("want observe then finish; ops=%v", ops)
			}
			var finishN int
			var finishOutcome string
			for _, op := range ops[1:] {
				if len(op) > 7 && op[:7] == "finish:" {
					finishN++
					finishOutcome = op[7:]
				}
			}
			if finishN != 1 || response.StreamOutcome(finishOutcome) != response.OutcomeClosed {
				t.Fatalf("want exactly Finish(closed); ops=%v", ops)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("Finish callback never ran; ops=%v", ops)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestCycleFinalStreamObservation_precommitOpenFailClosedSurfaces(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-cycle-open",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	factory := &nthOpenFailFactory{failAfter: 1, mode: sdkhooks.FailClosed}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	bus := hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		CompletionGates:         []completion.Gate{equalReplaceGate{}},
		StreamObserverFactories: []response.StreamObserverFactory{factory},
	})
	rs := &retryRecvStream{
		executor: ex,
		bus:      bus,
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{ID: "cycle-open", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-cycle-open",
			aLegID:   aLegID,
		}),
		bleg:       b2bua.BLegRecord{BLegID: "b-leg-cycle-open", Seq: 1},
		cand:       authorityCandidate(),
		authority:  testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()),
		accounting: newAttemptAccountingTracker(time.Unix(1, 0)),
	}
	if err := rs.openFinalStreamObservation(context.Background()); err != nil {
		t.Fatalf("initial open: %v", err)
	}
	_, cont, err := rs.handleRecvSuccess(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
	if err == nil {
		t.Fatal("precommit fail-closed Open after gate_replaced must surface")
	}
	if cont {
		t.Fatal("cont=false on open failure")
	}
	observers := factory.snapshot()
	if len(observers) != 1 {
		t.Fatalf("original lifecycle opens=%d", len(observers))
	}
	observers[0].mu.Lock()
	outcomes := append([]response.StreamOutcome(nil), observers[0].outcomes...)
	observers[0].mu.Unlock()
	if len(outcomes) != 1 || outcomes[0] != response.OutcomeGateReplaced {
		t.Fatalf("original must Finish(gate_replaced); got %#v", outcomes)
	}
}

func TestCycleFinalStreamObservation_postcommitOpenFailClosedBestEffort(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-cycle-post",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	factory := &nthOpenFailFactory{failAfter: 1, mode: sdkhooks.FailClosed}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	bus := hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		CompletionGates:         []completion.Gate{equalReplaceGate{}},
		StreamObserverFactories: []response.StreamObserverFactory{factory},
	})
	rs := &retryRecvStream{
		executor: ex,
		bus:      bus,
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{ID: "cycle-post", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-cycle-post",
			aLegID:   aLegID,
		}),
		bleg:       b2bua.BLegRecord{BLegID: "b-leg-cycle-post", Seq: 1},
		cand:       authorityCandidate(),
		authority:  testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()),
		accounting: newAttemptAccountingTracker(time.Unix(1, 0)),
	}
	if err := rs.openFinalStreamObservation(context.Background()); err != nil {
		t.Fatalf("initial open: %v", err)
	}
	rs.markCommitted()
	first, cont, err := rs.handleRecvSuccess(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
	if err != nil || cont {
		t.Fatalf("postcommit open failure must stay best-effort; err=%v cont=%v", err, cont)
	}
	if first.Kind != lipapi.EventResponseFinished {
		t.Fatalf("want response_finished; got %#v", first)
	}
}

func TestEmitClientFacingObserved_failClosedObserveAbortsFinishFailed(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-obs-fail",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	inner := &emitTestObserver{}
	factory := &failClosedObserveFactory{obs: inner, err: errors.New("observe boom")}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	bus := hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		StreamObserverFactories: []response.StreamObserverFactory{factory},
	})
	rs := &retryRecvStream{
		executor: ex,
		bus:      bus,
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{ID: "obs-fail", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-obs-fail",
			aLegID:   aLegID,
		}),
		bleg:       b2bua.BLegRecord{BLegID: "b-leg-obs-fail", Seq: 1},
		cand:       authorityCandidate(),
		authority:  testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()),
		accounting: newAttemptAccountingTracker(time.Unix(1, 0)),
	}
	if err := rs.openFinalStreamObservation(context.Background()); err != nil {
		t.Fatalf("open: %v", err)
	}
	_, cont, err := rs.handleRecvSuccess(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "x"})
	if err == nil {
		t.Fatal("want fail-closed Observe error")
	}
	if cont {
		t.Fatal("cont=false")
	}
	inner.mu.Lock()
	outcomes := append([]response.StreamOutcome(nil), inner.outcomes...)
	inner.mu.Unlock()
	if len(outcomes) != 1 || outcomes[0] != response.OutcomeFailed {
		t.Fatalf("any aborting Observe must Finish(failed); outcomes=%#v", outcomes)
	}
	if !rs.isFinished() {
		t.Fatal("fail-closed Observe abort must markFinished so further Recv cannot continue")
	}
	if _, err2 := rs.Recv(context.Background()); !errors.Is(err2, io.EOF) {
		t.Fatalf("post-abort Recv must be terminal EOF, got %v", err2)
	}
}

func TestIdleEOFRecoveryWarning_observedViaEmitClientFacing(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-idle-warn",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	factory := &emitTestObserverFactory{}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	bus := hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		StreamObserverFactories: []response.StreamObserverFactory{factory},
	})
	start := time.Unix(1, 0)
	rs := &retryRecvStream{
		executor: ex,
		bus:      bus,
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{ID: "idle-warn", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-idle-warn",
			aLegID:   aLegID,
		}),
		bleg:      b2bua.BLegRecord{BLegID: "b-leg-idle-warn", Seq: 1},
		cand:      authorityCandidate(),
		authority: testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()),
		seenEvents: []lipapi.Event{
			{Kind: lipapi.EventTextDelta, Delta: "hello"},
		},
		recoverPolicy: streamrecovery.NewPolicy(streamrecovery.Config{
			Enabled:     true,
			IdleTimeout: time.Second,
			EmitWarning: true,
		}, start),
		accounting: newAttemptAccountingTracker(start),
	}
	rs.visibleText.WriteString("hello")
	rs.recoverPolicy.ObserveClientEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"}, start.Add(time.Second))
	if err := rs.openFinalStreamObservation(context.Background()); err != nil {
		t.Fatalf("open: %v", err)
	}

	ev, cont, err := rs.handleRecvError(
		context.Background(),
		context.Background(),
		context.DeadlineExceeded,
		idleContextDeadline{active: true, parent: context.Background()},
		ttftContextDeadline{},
	)
	if err != nil || cont {
		t.Fatalf("idle recovery: err=%v cont=%v", err, cont)
	}
	if ev.Kind != lipapi.EventWarning || ev.WarningCode != "proxy_stream_recovery" {
		t.Fatalf("want recovery warning; got %#v", ev)
	}
	observers := factory.snapshot()
	if len(observers) == 0 {
		t.Fatal("expected observer")
	}
	obs := observers[0]
	obs.mu.Lock()
	sawWarn := false
	for _, e := range obs.events {
		if e.Kind == lipapi.EventWarning && e.WarningCode == "proxy_stream_recovery" {
			sawWarn = true
		}
	}
	obs.mu.Unlock()
	if !sawWarn {
		t.Fatal("idle recovery warning must be Observed via emitClientFacingObserved")
	}

	// EOF recovery warning path
	factory2 := &emitTestObserverFactory{}
	ex2, _, aLegID2 := newAuthorityRuntimeTestExecutor(t, auth)
	bus2 := hooks.New(hooks.Config{})
	ex2.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus2, extensions.SnapshotOptions{
		StreamObserverFactories: []response.StreamObserverFactory{factory2},
	})
	rs2 := &retryRecvStream{
		executor: ex2,
		bus:      bus2,
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{ID: "eof-warn", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			traceID:  "trace-eof-warn",
			aLegID:   aLegID2,
		}),
		bleg:      b2bua.BLegRecord{BLegID: "b-leg-eof-warn", Seq: 1},
		cand:      authorityCandidate(),
		authority: testAuthorityLifecycle(ex2, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult}, authorityCandidate()),
		seenEvents: []lipapi.Event{
			{Kind: lipapi.EventTextDelta, Delta: "hello"},
		},
		recoverPolicy: streamrecovery.NewPolicy(streamrecovery.Config{
			Enabled:     true,
			IdleTimeout: time.Second,
			EmitWarning: true,
		}, start),
		accounting: newAttemptAccountingTracker(start),
	}
	rs2.visibleText.WriteString("hello")
	rs2.recoverPolicy.ObserveClientEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"}, start.Add(time.Second))
	if err := rs2.openFinalStreamObservation(context.Background()); err != nil {
		t.Fatalf("open eof: %v", err)
	}
	ev2, err := rs2.handleRecvEOF(context.Background())
	if err != nil {
		t.Fatalf("handleRecvEOF: %v", err)
	}
	if ev2.Kind != lipapi.EventWarning || ev2.WarningCode != "proxy_stream_recovery" {
		t.Fatalf("want EOF recovery warning; got %#v", ev2)
	}
	obs2 := factory2.snapshot()[0]
	obs2.mu.Lock()
	sawEOFWarn := false
	for _, e := range obs2.events {
		if e.Kind == lipapi.EventWarning && e.WarningCode == "proxy_stream_recovery" {
			sawEOFWarn = true
		}
	}
	obs2.mu.Unlock()
	if !sawEOFWarn {
		t.Fatal("EOF recovery warning must be Observed via emitClientFacingObserved")
	}
}
