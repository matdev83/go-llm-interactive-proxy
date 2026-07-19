package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

func TestPhase42_CloseWinsWhileRecvFinishes_NoAttemptSuccess(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-close-finish",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	cand := authorityCandidate()
	state := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(5),
		admissionResult: auth.admitResult,
	}
	rs := &retryRecvStream{
		executor:   ex,
		bus:        hooks.New(hooks.Config{}),
		baseline:   lipapi.Call{ID: "req-close-finish", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
		bleg:       b2bua.BLegRecord{BLegID: "b-close-finish", Seq: 1},
		cand:       cand,
		authority:  testAuthorityLifecycle(ex, state, cand),
		traceID:    "trace-close-finish",
		aLegID:     aLegID,
		accounting: newAttemptAccountingTracker(time.Unix(1, 0)),
		seenEvents: []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "hi"}},
	}
	rs.markCommitted()
	rs.ensureTerminals()

	closeDone := make(chan struct{})
	go func() {
		_ = rs.runStreamTerminal(context.Background(), sdk.CommandClose, func(context.Context) error {
			rs.persistCancellationBilling(context.Background(), "client close")
			rs.markFinished()
			return nil
		})
		close(closeDone)
	}()
	<-closeDone

	finish := lipapi.Event{Kind: lipapi.EventResponseFinished}
	_, ok, err := rs.finalizeResponseFinishedAuthority(context.Background(), finish)
	if ok {
		t.Fatal("NormalFinish must not report ok after Close won")
	}
	if err == nil {
		t.Fatal("NormalFinish loser must surface cancel/error")
	}

	pm, _ := rs.recvHookMeta()
	ev, cont, pathErr := rs.handleResponseFinishedPath(context.Background(), finish, pm)
	if cont {
		t.Fatal("must not continue after lost finish")
	}
	if pathErr == nil {
		t.Fatal("handleResponseFinishedPath must error when Close already won")
	}
	if !errors.Is(pathErr, leglifecycle.ErrALegCanceled) {
		t.Fatalf("pathErr=%v want ErrALegCanceled (not bare context.Canceled)", pathErr)
	}
	if errors.Is(pathErr, context.Canceled) && !errors.Is(pathErr, leglifecycle.ErrALegCanceled) {
		t.Fatalf("must not surface bare context.Canceled: %v", pathErr)
	}
	if ev.Kind == lipapi.EventResponseFinished {
		t.Fatal("must not emit response_finished after Close won")
	}
}

func TestPhase42_CloseThenFinishDelivery_NoBareContextCanceled(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-recv-after-close",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	cand := authorityCandidate()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg("a-recv-after-close")
	rs := &retryRecvStream{
		executor:   ex,
		bus:        hooks.New(hooks.Config{}),
		baseline:   lipapi.Call{ID: "req-recv-after-close", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
		bleg:       b2bua.BLegRecord{BLegID: "b-recv-after-close", Seq: 1},
		cand:       cand,
		authority:  testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(5), admissionResult: auth.admitResult}, cand),
		traceID:    "trace-recv-after-close",
		aLegID:     aLegID,
		aScope:     aScope,
		accounting: newAttemptAccountingTracker(time.Unix(1, 0)),
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	// Ignore Cancel so Close cannot wake Recv via ctx; only releaseTail delivers finish
	// after Close has already claimed CommandClose (the flaky parallel_race path).
	rs.storeInner(&finishAfterReleaseStream{
		entered: entered,
		release: release,
		finish:  lipapi.Event{Kind: lipapi.EventResponseFinished},
	})
	rs.ensureTerminals()

	recvDone := make(chan error, 1)
	go func() {
		_, err := rs.Recv(context.Background())
		recvDone <- err
	}()
	<-entered
	if err := rs.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
	err := <-recvDone
	if err == nil {
		t.Fatal("expected terminal error after Close")
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, leglifecycle.ErrALegCanceled) {
		t.Fatalf("got %v want EOF or ErrALegCanceled", err)
	}
	if errors.Is(err, context.Canceled) && !errors.Is(err, leglifecycle.ErrALegCanceled) {
		t.Fatalf("bare context.Canceled is not an allowed Close/Recv race outcome: %v", err)
	}
}

type finishAfterReleaseStream struct {
	entered chan<- struct{}
	release <-chan struct{}
	finish  lipapi.Event
	done    bool
}

func (s *finishAfterReleaseStream) Recv(context.Context) (lipapi.Event, error) {
	if s.done {
		return lipapi.Event{}, io.EOF
	}
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	<-s.release
	s.done = true
	return s.finish, nil
}

func (s *finishAfterReleaseStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{}
}

func (s *finishAfterReleaseStream) Close() error { return nil }

func TestPhase42_EncoderFailureCompetesBeforeNormalFinish(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-enc",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	ex.SecureSessionRecordingMandatory = true
	encErr := errors.New("encoder boom")
	ex.SecureSessionRecorder = failingSecureRecorderStub{err: encErr}
	ex.StreamUsage = nil

	cand := authorityCandidate()
	state := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(5),
		admissionResult: auth.admitResult,
	}
	rs := &retryRecvStream{
		executor:     ex,
		bus:          hooks.New(hooks.Config{}),
		baseline:     lipapi.Call{ID: "req-enc", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
		bleg:         b2bua.BLegRecord{BLegID: "b-enc", Seq: 1},
		cand:         cand,
		authority:    testAuthorityLifecycle(ex, state, cand),
		traceID:      "trace-enc",
		aLegID:       aLegID,
		accounting:   newAttemptAccountingTracker(time.Unix(1, 0)),
		seenEvents:   []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "x"}},
		secureTurnOK: true,
	}
	rs.markCommitted()
	rs.ensureTerminals()

	finish := lipapi.Event{Kind: lipapi.EventResponseFinished}
	pm, _ := rs.recvHookMeta()
	_, _, err := rs.handleResponseFinishedPath(context.Background(), finish, pm)
	if !errors.Is(err, encErr) {
		t.Fatalf("err=%v want encoder", err)
	}
	out, ok := rs.requestTerm.Owner().Outcome()
	if !ok || out.Command != sdk.CommandFrontendEncoderFailure {
		t.Fatalf("terminal outcome=%+v ok=%v want frontend_encoder_failure", out, ok)
	}
	if auth.settleCalls.Load() != 0 && out.Command == sdk.CommandNormalFinish {
		t.Fatal("NormalFinish must not win when encoder preflight fails")
	}
	// Encoder must claim before any NormalFinish settlement.
	if out.Command == sdk.CommandNormalFinish {
		t.Fatal("NormalFinish must not be the published command")
	}
}

func TestPhase42_CancelTerminalizesRequest(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-cancel",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	cand := authorityCandidate()
	rs := &retryRecvStream{
		executor:   ex,
		bus:        hooks.New(hooks.Config{}),
		baseline:   lipapi.Call{ID: "req-cancel", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
		bleg:       b2bua.BLegRecord{BLegID: "b-cancel", Seq: 1},
		cand:       cand,
		authority:  testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(5), admissionResult: auth.admitResult}, cand),
		traceID:    "trace-cancel",
		aLegID:     aLegID,
		accounting: newAttemptAccountingTracker(time.Unix(1, 0)),
	}
	rs.ensureTerminals()
	r := rs.runStreamTerminal(context.Background(), sdk.CommandCancel, func(cctx context.Context) error {
		rs.persistCancellationBilling(cctx, "context canceled")
		rs.markFinished()
		return nil
	})
	if !r.Won || !rs.requestTerm.Owner().State().IsTerminal() {
		t.Fatalf("cancel terminalize: %+v state=%q", r, rs.requestTerm.Owner().State())
	}
}

func TestPhase42_NoRetryAfterOutput_GateReplacement(t *testing.T) {
	t.Parallel()
	rs := &retryRecvStream{}
	rs.ensureTerminals()
	rs.markCommitted()
	r := rs.runStreamTerminal(context.Background(), sdk.CommandGateReplacement, nil)
	if r.Won || !errors.Is(r.Err, sdk.ErrOutputCommitted) {
		t.Fatalf("got %+v", r)
	}
	if rs.requestTerm.Owner().State() != sdk.StateOpen {
		t.Fatalf("open+committed rejection must leave owner open, state=%q", rs.requestTerm.Owner().State())
	}
}

func TestPhase42_ResponsePartHook_RoutesThroughTerminal(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "res-part",
			ReservedAmount: authorityInputAmount(5),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	hookErr := errors.New("part hook boom")
	bus := hooks.New(hooks.Config{
		ResponsePartHooks: []sdkhooks.ResponsePartHook{failingResponsePartHookStub{err: hookErr}},
	})
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	cand := authorityCandidate()
	rs := &retryRecvStream{
		executor:   ex,
		bus:        bus,
		baseline:   lipapi.Call{ID: "req-part", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
		bleg:       b2bua.BLegRecord{BLegID: "b-part", Seq: 1},
		cand:       cand,
		authority:  testAuthorityLifecycle(ex, attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(5), admissionResult: auth.admitResult}, cand),
		traceID:    "trace-part",
		aLegID:     aLegID,
		accounting: newAttemptAccountingTracker(time.Unix(1, 0)),
	}
	rs.ensureTerminals()
	_, cont, err := rs.handleRecvSuccess(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "x"})
	if !errors.Is(err, hookErr) || cont {
		t.Fatalf("err=%v cont=%v", err, cont)
	}
	if rs.requestTerm == nil || !rs.requestTerm.Owner().State().IsTerminal() {
		t.Fatal("response part failure must terminalize request")
	}
}

func TestPhase42_EventsMu_ClearAndSnapshot(t *testing.T) {
	t.Parallel()
	rs := &retryRecvStream{
		seenEvents: []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "a"}},
	}
	rs.visibleText.WriteString("a")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = rs.seenEventsCopy()
	}()
	go func() {
		defer wg.Done()
		rs.clearClientAccumulators()
	}()
	wg.Wait()
	if got := rs.seenEventsCopy(); len(got) != 0 {
		t.Fatalf("cleared accumulators len=%d", len(got))
	}
}
