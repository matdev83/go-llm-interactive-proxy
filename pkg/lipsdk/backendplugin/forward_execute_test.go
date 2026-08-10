package backendplugin_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestForwardExecute_ForwardsEventsVerbatim(t *testing.T) {
	t.Parallel()

	events := []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "hel"},
		{Kind: lipapi.EventTextDelta, Delta: "lo"},
		{Kind: lipapi.EventResponseFinished},
	}
	ms := &scriptedManaged{events: events}
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))

	err := backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call lipapi.Call) (lipapi.ManagedEventStream, error) {
		if ctx == nil {
			t.Fatal("open ctx must not be nil")
		}
		if inv.RequestID != "r1" {
			t.Fatalf("inv.RequestID=%q", inv.RequestID)
		}
		if call.ID != "r1" {
			t.Fatalf("call.ID=%q", call.ID)
		}
		return ms, nil
	})
	if err != nil {
		t.Fatalf("ForwardExecute: %v", err)
	}

	if len(stream.sent) < 1 || stream.sent[0].Kind != backendplugin.ServerFrameAccepted {
		t.Fatalf("first frame=%v, want accepted", stream.sent)
	}
	gotEvents := make([]lipapi.Event, 0, len(events))
	var sawTerminal bool
	for i, f := range stream.sent[1:] {
		wantSeq := uint64(i + 1)
		if f.Sequence != wantSeq {
			t.Fatalf("frame[%d].Sequence=%d, want %d", i, f.Sequence, wantSeq)
		}
		switch f.Kind {
		case backendplugin.ServerFrameEvent:
			if f.Event == nil {
				t.Fatalf("frame[%d]: nil event", i)
			}
			gotEvents = append(gotEvents, lipapi.Event{
				Kind:  f.Event.Kind,
				Delta: derefStr(f.Event.Delta),
			})
		case backendplugin.ServerFrameTerminal:
			sawTerminal = true
			if f.Terminal == nil || f.Terminal.Status != backendplugin.TerminalSuccess {
				t.Fatalf("terminal=%+v, want success", f.Terminal)
			}
		default:
			t.Fatalf("unexpected frame kind %q", f.Kind)
		}
	}
	if !sawTerminal {
		t.Fatal("expected terminal success after upstream EOF")
	}
	if len(gotEvents) != len(events) {
		t.Fatalf("got %d events, want %d (%v)", len(gotEvents), len(events), gotEvents)
	}
	for i := range events {
		if gotEvents[i].Kind != events[i].Kind || gotEvents[i].Delta != events[i].Delta {
			t.Fatalf("event[%d]=%+v, want %+v", i, gotEvents[i], events[i])
		}
	}
}

func TestForwardExecute_ForwardsOpeningAccountingEvidenceBeforeCanonicalEvents(t *testing.T) {
	t.Parallel()

	input := int64(41)
	usage := backendplugin.AccountingEvidence{
		InputTokens: &input,
		Presence:    lipapi.UsagePresence{InputTokens: true},
		Source:      backendplugin.AccountingSourceProviderReported,
		Authority:   backendplugin.AccountingAuthorityAuthoritative,
		Plane:       backendplugin.AccountingPlaneProviderBillable,
		DedupeKey:   "compaction:turn-1",
	}
	ms := &evidenceManaged{evidence: []backendplugin.AccountingEvidence{usage}, events: []lipapi.Event{{Kind: lipapi.EventResponseFinished}}}
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))

	if err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, lipapi.Call) (lipapi.ManagedEventStream, error) {
		return ms, nil
	}); err != nil {
		t.Fatalf("ForwardExecute: %v", err)
	}
	if len(stream.sent) < 3 {
		t.Fatalf("frames = %d, want accepted, evidence, event, terminal", len(stream.sent))
	}
	if stream.sent[1].Kind != backendplugin.ServerFrameAccountingEvidence {
		t.Fatalf("first post-accepted frame = %q, want accounting evidence", stream.sent[1].Kind)
	}
	if stream.sent[1].Accounting == nil || stream.sent[1].Accounting.DedupeKey != usage.DedupeKey {
		t.Fatalf("evidence frame = %#v", stream.sent[1].Accounting)
	}
	if stream.sent[2].Kind != backendplugin.ServerFrameEvent {
		t.Fatalf("canonical frame after evidence = %q", stream.sent[2].Kind)
	}
	for i, frame := range stream.sent[1:] {
		if frame.Sequence != uint64(i+1) {
			t.Fatalf("frame %d sequence = %d, want %d", i+1, frame.Sequence, i+1)
		}
	}
}

func TestForwardExecute_CancelsUpstreamWhenStreamContextDone(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	ms := &blockingManaged{unblocked: make(chan struct{})}
	stream := newFakeExecuteStream(ctx, validStartFrame(t))

	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, lipapi.Call) (lipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	waitUntil(t, 2*time.Second, func() bool { return ms.recvEntered.Load() })
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected ForwardExecute to return on stream cancel")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ForwardExecute did not return after stream context cancel")
	}

	waitUntil(t, 2*time.Second, func() bool { return ms.cancelCalled.Load() })
	if got := ms.cancelCause.Kind; got != lipapi.CancelContextDone {
		t.Fatalf("Cancel cause kind=%q, want %q", got, lipapi.CancelContextDone)
	}
	if got := ms.cancelCause.Detail; got != "plugin_cancel" {
		t.Fatalf("Cancel detail=%q, want plugin_cancel", got)
	}
}

func TestForwardExecute_UpstreamErrorReturnsWithoutTerminal(t *testing.T) {
	t.Parallel()

	upstreamErr := errors.New("upstream boom")
	ms := &scriptedManaged{err: upstreamErr}
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))

	err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, lipapi.Call) (lipapi.ManagedEventStream, error) {
		return ms, nil
	})
	if !errors.Is(err, upstreamErr) {
		t.Fatalf("err=%v, want %v", err, upstreamErr)
	}
	for _, f := range stream.sent {
		if f.Kind == backendplugin.ServerFrameTerminal {
			t.Fatalf("unexpected terminal on upstream error: %+v", f)
		}
	}
}

func TestForwardExecute_OpenErrorPropagates(t *testing.T) {
	t.Parallel()

	openErr := errors.New("open failed")
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))
	err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, lipapi.Call) (lipapi.ManagedEventStream, error) {
		return nil, openErr
	})
	if !errors.Is(err, openErr) {
		t.Fatalf("err=%v, want %v", err, openErr)
	}
}

func TestForwardExecute_NilOpen(t *testing.T) {
	t.Parallel()
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))
	err := backendplugin.ForwardExecute(stream, nil)
	if err == nil {
		t.Fatal("expected error for nil open")
	}
}

func TestForwardExecute_WrappedEOFEmitsTerminalSuccess(t *testing.T) {
	t.Parallel()

	ms := &wrappedEOFManaged{}
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))

	err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, lipapi.Call) (lipapi.ManagedEventStream, error) {
		return ms, nil
	})
	if err != nil {
		t.Fatalf("ForwardExecute: %v", err)
	}
	var sawTerminal bool
	for _, f := range stream.sent {
		if f.Kind == backendplugin.ServerFrameTerminal {
			sawTerminal = true
			if f.Terminal == nil || f.Terminal.Status != backendplugin.TerminalSuccess {
				t.Fatalf("terminal=%+v, want success", f.Terminal)
			}
		}
	}
	if !sawTerminal {
		t.Fatal("wrapped io.EOF must emit terminal success")
	}
}

func TestForwardExecute_NilStreamFromOpen(t *testing.T) {
	t.Parallel()
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))
	err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, lipapi.Call) (lipapi.ManagedEventStream, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected error for nil stream from open")
	}
	if !strings.Contains(err.Error(), "nil stream") {
		t.Fatalf("err=%v, want nil stream error", err)
	}
}

func TestForwardExecute_CloseExactlyOnceOnCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	ms := &closeCountManaged{unblocked: make(chan struct{})}
	stream := newFakeExecuteStream(ctx, validStartFrame(t))

	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, lipapi.Call) (lipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	waitUntil(t, 2*time.Second, func() bool { return ms.recvEntered.Load() })
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected ForwardExecute to return on stream cancel")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ForwardExecute did not return after stream context cancel")
	}

	waitUntil(t, 2*time.Second, func() bool { return ms.closeCalls.Load() == 1 })
	if got := ms.closeCalls.Load(); got != 1 {
		t.Fatalf("Close calls=%d, want exactly 1", got)
	}
}

func validStartFrame(t *testing.T) backendplugin.ClientFrame {
	t.Helper()
	text := "hi"
	inv := backendplugin.Invocation{
		RequestID: "r1", AttemptID: "a1", ALegID: "aleg", BLegID: "bleg",
		CanonicalModelID: "m1",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	return backendplugin.ClientFrame{
		Kind: backendplugin.ClientFrameStart, InstanceID: "i1", Invocation: &inv,
	}
}

type fakeExecuteStream struct {
	ctx   context.Context
	inbox []backendplugin.ClientFrame
	mu    sync.Mutex
	sent  []backendplugin.ServerFrame
}

func newFakeExecuteStream(ctx context.Context, start backendplugin.ClientFrame) *fakeExecuteStream {
	return &fakeExecuteStream{ctx: ctx, inbox: []backendplugin.ClientFrame{start}}
}

func (f *fakeExecuteStream) Context() context.Context { return f.ctx }

func (f *fakeExecuteStream) Recv() (backendplugin.ClientFrame, error) {
	if len(f.inbox) == 0 {
		return backendplugin.ClientFrame{}, io.EOF
	}
	fr := f.inbox[0]
	f.inbox = f.inbox[1:]
	return fr, nil
}

func (f *fakeExecuteStream) Send(frame backendplugin.ServerFrame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, frame)
	return nil
}

type scriptedManaged struct {
	events []lipapi.Event
	err    error
	idx    int
	closed atomic.Bool
}

type evidenceManaged struct {
	evidence []backendplugin.AccountingEvidence
	events   []lipapi.Event
	idx      int
}

func (m *evidenceManaged) Recv(context.Context) (lipapi.Event, error) {
	if m.idx >= len(m.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := m.events[m.idx]
	m.idx++
	return ev, nil
}

func (m *evidenceManaged) DrainAccountingEvidence() []backendplugin.AccountingEvidence {
	evidence := append([]backendplugin.AccountingEvidence(nil), m.evidence...)
	m.evidence = nil
	return evidence
}

func (m *evidenceManaged) Close() error { return nil }

func (m *evidenceManaged) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func (m *scriptedManaged) Recv(context.Context) (lipapi.Event, error) {
	if m.err != nil && m.idx >= len(m.events) {
		return lipapi.Event{}, m.err
	}
	if m.idx >= len(m.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := m.events[m.idx]
	m.idx++
	return ev, nil
}

func (m *scriptedManaged) Close() error {
	m.closed.Store(true)
	return nil
}

func (m *scriptedManaged) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

// blockingManaged Recv ignores the passed context and only unblocks when Cancel is called,
// proving ForwardExecute observes stream cancellation and invokes Cancel (codex-grade).
type blockingManaged struct {
	recvEntered  atomic.Bool
	cancelCalled atomic.Bool
	cancelCause  lipapi.CancelCause
	unblocked    chan struct{}
	closeOnce    sync.Once
}

func (m *blockingManaged) Recv(context.Context) (lipapi.Event, error) {
	m.recvEntered.Store(true)
	<-m.unblocked
	return lipapi.Event{}, context.Canceled
}

func (m *blockingManaged) Close() error { return nil }

func (m *blockingManaged) Cancel(_ context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	m.cancelCause = cause
	m.cancelCalled.Store(true)
	m.closeOnce.Do(func() { close(m.unblocked) })
	return lipapi.CancelResult{Mode: lipapi.CancelModeProvider}
}

type wrappedEOFManaged struct{}

func (m *wrappedEOFManaged) Recv(context.Context) (lipapi.Event, error) {
	return lipapi.Event{}, fmt.Errorf("wrapped: %w", io.EOF)
}

func (m *wrappedEOFManaged) Close() error { return nil }

func (m *wrappedEOFManaged) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

type closeCountManaged struct {
	recvEntered atomic.Bool
	closeCalls  atomic.Int32
	unblocked   chan struct{}
}

func (m *closeCountManaged) Recv(ctx context.Context) (lipapi.Event, error) {
	m.recvEntered.Store(true)
	select {
	case <-ctx.Done():
		return lipapi.Event{}, ctx.Err()
	case <-m.unblocked:
		return lipapi.Event{}, context.Canceled
	}
}

func (m *closeCountManaged) Close() error {
	m.closeCalls.Add(1)
	return nil
}

func (m *closeCountManaged) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func waitUntil(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
