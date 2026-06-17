package runtime_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func newTestClock(t0 time.Time) (func() time.Time, func(d time.Duration)) {
	var mu sync.Mutex
	now := t0
	return func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		}, func(d time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			now = now.Add(d)
		}
}

func parallelStore(t *testing.T) b2bua.Store {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func parallelBackend(events []lipapi.Event) execbackend.Backend {
	return execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return lipapi.NewFixedEventStream(events), nil
		},
	}
}

func delayedBackend(delay time.Duration, events []lipapi.Event) execbackend.Backend {
	return execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return &delayedStream{delay: delay, events: events, ctx: ctx}, nil
		},
	}
}

func delayedTailBackend(tailDelay time.Duration, text string) execbackend.Backend {
	return execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return &tailDelayedStream{
				delay: tailDelay,
				events: []lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: text},
					{Kind: lipapi.EventResponseFinished},
				},
			}, nil
		},
	}
}

type delayedStream struct {
	delay  time.Duration
	events []lipapi.Event
	idx    int
	ctx    context.Context
}

func (d *delayedStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if d.idx == 0 {
		select {
		case <-time.After(d.delay):
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		}
	}
	if d.idx >= len(d.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := d.events[d.idx]
	d.idx++
	return ev, nil
}

func (d *delayedStream) Cancel(_ context.Context, _ lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{}
}

func (d *delayedStream) Close() error { return nil }

type tailDelayedStream struct {
	delay  time.Duration
	events []lipapi.Event
	idx    int
}

func (t *tailDelayedStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if t.idx == len(t.events)-1 {
		select {
		case <-time.After(t.delay):
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		}
	}
	if t.idx >= len(t.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := t.events[t.idx]
	t.idx++
	return ev, nil
}

func (t *tailDelayedStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{}
}

func (t *tailDelayedStream) Close() error { return nil }

func errorBackend(err error) execbackend.Backend {
	return execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return nil, err
		},
	}
}

func parallelCall(selector string) *lipapi.Call {
	return &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: selector},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
}

func completionEvents(text string) []lipapi.Event {
	return []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: text},
		{Kind: lipapi.EventResponseFinished},
	}
}

func TestParallelRace_FirstNonWhitespaceTokenWins(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"slow": delayedBackend(200*time.Millisecond, completionEvents("slow-response")),
			"fast": parallelBackend(completionEvents("fast-response")),
		},
		Rand: routing.NewSeededRng(1),
	}
	s, err := ex.Execute(context.Background(), parallelCall("slow:model!fast:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "fast-response" {
		t.Fatalf("winner text: %q want fast-response", col.Text.String())
	}
}

func TestParallelRace_WhitespaceIgnoredForWinnerElection(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	wsEvents := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "   "},
		{Kind: lipapi.EventTextDelta, Delta: "  \n\t  "},
	}
	realEvents := completionEvents("real-answer")
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"ws":   parallelBackend(wsEvents),
			"real": parallelBackend(realEvents),
		},
		Rand: routing.NewSeededRng(1),
	}
	s, err := ex.Execute(context.Background(), parallelCall("ws:model!real:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "real-answer" {
		t.Fatalf("winner text: %q want real-answer", col.Text.String())
	}
}

func TestParallelRace_HandicapSchedulingStartsHighFirst(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var openOrder []string
	var mu sync.Mutex
	trackingBackend := func(name string, events []lipapi.Event) execbackend.Backend {
		return execbackend.Backend{
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				mu.Lock()
				openOrder = append(openOrder, name)
				mu.Unlock()
				return lipapi.NewFixedEventStream(events), nil
			},
		}
	}
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"a": trackingBackend("a", completionEvents("a-resp")),
			"b": trackingBackend("b", completionEvents("b-resp")),
			"c": trackingBackend("c", completionEvents("c-resp")),
		},
		Rand: routing.NewSeededRng(1),
	}
	s, err := ex.Execute(context.Background(), parallelCall("[handicap=3]a:model![handicap=1]b:model!c:model"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lipapi.Collect(context.Background(), s)

	mu.Lock()
	defer mu.Unlock()
	if len(openOrder) == 0 {
		t.Fatal("expected at least one backend open")
	}
	if openOrder[0] != "a" {
		t.Fatalf("highest handicap (a) should start first, got order: %v", openOrder)
	}
}

func TestParallelRace_HandicapShortCircuitOnEarlyWinner(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var opens int32
	fastBackend := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			atomic.AddInt32(&opens, 1)
			return lipapi.NewFixedEventStream(completionEvents("fast")), nil
		},
	}
	slowBackend := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			atomic.AddInt32(&opens, 1)
			return lipapi.NewFixedEventStream(completionEvents("slow")), nil
		},
	}
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"fast": fastBackend,
			"slow": slowBackend,
		},
		Rand: routing.NewSeededRng(1),
	}
	s, err := ex.Execute(context.Background(), parallelCall("[handicap=10]fast:model!slow:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "fast" {
		t.Fatalf("text: %q want fast", col.Text.String())
	}
	if atomic.LoadInt32(&opens) != 1 {
		t.Fatalf("expected only 1 open (fast winner short-circuits), got %d", opens)
	}
}

func TestParallelRace_MaxAttemptsBoundsParallelOpensDeterministically(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var opensA int32
	var opensB int32
	ex := &runtime.Executor{
		Store:       st,
		Bus:         hooks.New(hooks.Config{}),
		MaxAttempts: 1,
		Backends: map[string]execbackend.Backend{
			"a": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					atomic.AddInt32(&opensA, 1)
					return lipapi.NewFixedEventStream(completionEvents("a")), nil
				},
			},
			"b": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					atomic.AddInt32(&opensB, 1)
					return lipapi.NewFixedEventStream(completionEvents("b")), nil
				},
			},
		},
		Rand: routing.NewSeededRng(1),
	}
	s, err := ex.Execute(context.Background(), parallelCall("a:model!b:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if got := col.Text.String(); got != "a" {
		t.Fatalf("winner text: %q want a", got)
	}
	if atomic.LoadInt32(&opensA) != 1 {
		t.Fatalf("backend a opens: got %d want 1", atomic.LoadInt32(&opensA))
	}
	if atomic.LoadInt32(&opensB) != 0 {
		t.Fatalf("backend b opens: got %d want 0", atomic.LoadInt32(&opensB))
	}
}

func TestParallelRace_HandicapFastForwardOnTerminalFailure(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var opens int32
	failBackend := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			atomic.AddInt32(&opens, 1)
			return nil, errors.New("terminal failure")
		},
	}
	okBackend := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			atomic.AddInt32(&opens, 1)
			return lipapi.NewFixedEventStream(completionEvents("ok")), nil
		},
	}
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"fail": failBackend,
			"ok":   okBackend,
		},
		Rand: routing.NewSeededRng(1),
	}
	s, err := ex.Execute(context.Background(), parallelCall("[handicap=10]fail:model!ok:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "ok" {
		t.Fatalf("text: %q want ok (fast-forward after handicapped fail)", col.Text.String())
	}
}

func TestParallelRace_PerLegTTFTTimeoutElimination(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	slowEvents := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
	}
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"slow": delayedBackend(60*time.Second, slowEvents),
			"fast": parallelBackend(completionEvents("fast")),
		},
		Rand: routing.NewSeededRng(1),
	}
	s, err := ex.Execute(context.Background(), parallelCall("[ttft_timeout=1]slow:model!fast:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "fast" {
		t.Fatalf("text: %q want fast (slow should be TTFT-eliminated)", col.Text.String())
	}
}

func TestParallelRace_TTFTTimeoutActuallyKillsLeg(t *testing.T) {
	// Both backends are slow, but one has a 1s TTFT timeout and the other has a
	// 200ms first-token delay. Without TTFT enforcement the slow leg blocks forever;
	// with it the slow leg's context is cancelled and the slightly-delayed backend wins.
	t.Parallel()
	st := parallelStore(t)
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"stuck": delayedBackend(60*time.Second, completionEvents("stuck")),
			"ok":    delayedBackend(200*time.Millisecond, completionEvents("ok")),
		},
		Rand: routing.NewSeededRng(1),
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	s, err := ex.Execute(ctx, parallelCall("[ttft_timeout=1]stuck:model!ok:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "ok" {
		t.Fatalf("text: %q want ok (stuck leg should be killed by TTFT timeout)", col.Text.String())
	}
}

func TestParallelRace_KeepaliveEmittedWhileWaiting(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"slow": delayedTailBackend(2*time.Second, "ok"),
		},
		Rand: routing.NewSeededRng(1),
	}
	s, err := ex.Execute(context.Background(), parallelCall("slow:model!slow:model2"))
	if err != nil {
		t.Fatal(err)
	}
	ka, err := stream.NewKeepalive(s, stream.KeepaliveConfig{Interval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ka.Close() }()
	var sawKeepalive bool
	for {
		ev, err := ka.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == lipapi.EventWarning && ev.WarningCode == stream.KeepaliveEventCode {
			sawKeepalive = true
		}
		if ev.Kind == lipapi.EventResponseFinished {
			break
		}
	}
	if !sawKeepalive {
		t.Fatal("expected keepalive warning while waiting for winner stream")
	}
}

func TestParallelRace_CancelLosersBeforeClose(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var cancelCalled int32
	var loserOpened int32
	loserOpenedCh := make(chan struct{}, 8)
	cancelNotified := make(chan struct{}, 8)
	cancelableBackend := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			atomic.AddInt32(&loserOpened, 1)
			select {
			case loserOpenedCh <- struct{}{}:
			default:
			}
			return &cancelTrackingStream{
				events: completionEvents("loser"),
				ctx:    ctx,
				onCancel: func() {
					atomic.AddInt32(&cancelCalled, 1)
					select {
					case cancelNotified <- struct{}{}:
					default:
					}
				},
				delay: 2 * time.Second,
			}, nil
		},
	}
	slowWinnerBackend := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			for range 2 {
				select {
				case <-loserOpenedCh:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return lipapi.NewFixedEventStream(completionEvents("winner")), nil
		},
	}
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"winner": slowWinnerBackend,
			"loser1": cancelableBackend,
			"loser2": cancelableBackend,
		},
		Rand: routing.NewSeededRng(1),
	}
	s, err := ex.Execute(t.Context(), parallelCall("winner:model!loser1:model!loser2:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "winner" {
		t.Fatalf("text: %q want winner", col.Text.String())
	}
	for i := range 2 {
		select {
		case <-cancelNotified:
		case <-time.After(2 * time.Second):
			t.Fatalf("expected loser cancel notification %d/2", i+1)
		}
	}
	if atomic.LoadInt32(&loserOpened) < 2 {
		t.Fatalf("expected both loser legs opened, got %d", atomic.LoadInt32(&loserOpened))
	}
	if atomic.LoadInt32(&cancelCalled) < 2 {
		t.Fatalf("expected Cancel on both losers, got %d", atomic.LoadInt32(&cancelCalled))
	}
}

func TestParallelRace_CloseWhileRecvBlockedIsRaceSafe(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	releaseTail := make(chan struct{})
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"winner": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return &blockingTailStream{
						events: []lipapi.Event{
							{Kind: lipapi.EventResponseStarted},
							{Kind: lipapi.EventMessageStarted},
							{Kind: lipapi.EventTextDelta, Delta: "winner"},
							{Kind: lipapi.EventResponseFinished},
						},
						releaseTail: releaseTail,
					}, nil
				},
			},
			"loser": delayedBackend(2*time.Second, completionEvents("loser")),
		},
		Rand: routing.NewSeededRng(1),
	}
	s, err := ex.Execute(t.Context(), parallelCall("winner:model!loser:model"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ev, err := s.Recv(t.Context()); err != nil || ev.Kind != lipapi.EventTextDelta {
		t.Fatalf("winner text event = %+v, %v", ev, err)
	}
	recvDone := make(chan error, 1)
	go func() {
		_, err := s.Recv(t.Context())
		recvDone <- err
	}()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	close(releaseTail)
	select {
	case err := <-recvDone:
		if err != nil && !errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "a-leg canceled") {
			t.Fatalf("blocked recv returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked recv did not finish")
	}
}

type cancelTrackingStream struct {
	events   []lipapi.Event
	idx      int
	ctx      context.Context
	onCancel func()
	delay    time.Duration
}

func (c *cancelTrackingStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if c.idx == 0 && c.delay > 0 {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		}
	}
	if c.idx >= len(c.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := c.events[c.idx]
	c.idx++
	return ev, nil
}

func (c *cancelTrackingStream) Cancel(_ context.Context, _ lipapi.CancelCause) lipapi.CancelResult {
	if c.onCancel != nil {
		c.onCancel()
	}
	return lipapi.CancelResult{}
}

func (c *cancelTrackingStream) Close() error { return nil }

type blockingTailStream struct {
	events      []lipapi.Event
	idx         int
	releaseTail <-chan struct{}
}

func (s *blockingTailStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.idx == len(s.events)-1 {
		select {
		case <-s.releaseTail:
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		}
	}
	if s.idx >= len(s.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *blockingTailStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{}
}

func (s *blockingTailStream) Close() error { return nil }

func TestParallelRace_FailoverToNextArmWhenNoWinner(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"fail1": errorBackend(errors.New("fail1")),
			"fail2": errorBackend(errors.New("fail2")),
			"ok":    parallelBackend(completionEvents("fallback")),
		},
		Rand: routing.NewSeededRng(1),
	}
	s, err := ex.Execute(context.Background(), parallelCall("fail1:model!fail2:model|ok:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "fallback" {
		t.Fatalf("text: %q want fallback", col.Text.String())
	}
}

func TestParallelRace_AllLegFailuresSurfaceJoinedError(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"fail1": errorBackend(errors.New("fail one")),
			"fail2": errorBackend(errors.New("fail two")),
		},
		Rand: routing.NewSeededRng(1),
	}
	_, err := ex.Execute(context.Background(), parallelCall("fail1:model!fail2:model"))
	if err == nil {
		t.Fatal("expected execute error for all-failing parallel arm")
	}
	if !strings.Contains(err.Error(), "parallel race arm failed") {
		t.Fatalf("expected parallel aggregation context, got %v", err)
	}
	if !strings.Contains(err.Error(), "fail1:model") || !strings.Contains(err.Error(), "fail2:model") {
		t.Fatalf("expected candidate keys in joined error, got %v", err)
	}
}

func TestParallelRace_NoFailoverAfterWinnerOutputCommitted(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var fallbackOpens int32
	fallbackOpenedCh := make(chan struct{}, 8)
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"a": parallelBackend(completionEvents("winner")),
			"b": delayedBackend(250*time.Millisecond, completionEvents("other")),
			"fallback": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					atomic.AddInt32(&fallbackOpens, 1)
					select {
					case fallbackOpenedCh <- struct{}{}:
					default:
					}
					return lipapi.NewFixedEventStream(completionEvents("fallback")), nil
				},
			},
		},
		Rand: routing.NewSeededRng(1),
	}
	s, err := ex.Execute(context.Background(), parallelCall("a:model!b:model|fallback:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if got := col.Text.String(); got != "winner" {
		t.Fatalf("winner text: %q want winner", got)
	}
	select {
	case <-fallbackOpenedCh:
		t.Fatal("unexpected failover backend open after winner output committed")
	case <-time.After(300 * time.Millisecond):
	}
	if atomic.LoadInt32(&fallbackOpens) != 0 {
		t.Fatalf("fallback backend opened %d times; want 0", atomic.LoadInt32(&fallbackOpens))
	}
}

func TestParallelRace_ReasoningDeltaWins(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	reasoningEvents := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "thinking..."},
		{Kind: lipapi.EventTextDelta, Delta: "after-reasoning"},
		{Kind: lipapi.EventResponseFinished},
	}
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"reason": parallelBackend(reasoningEvents),
			"slow":   delayedBackend(500*time.Millisecond, completionEvents("slow")),
		},
		Rand: routing.NewSeededRng(1),
	}
	s, err := ex.Execute(context.Background(), parallelCall("reason:model!slow:model"))
	if err != nil {
		t.Fatal(err)
	}
	var sawReasoning bool
	for {
		ev, err := s.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == lipapi.EventReasoningDelta {
			sawReasoning = true
		}
		if ev.Kind == lipapi.EventResponseFinished {
			break
		}
	}
	if !sawReasoning {
		t.Fatal("expected reasoning_delta from winning stream")
	}
}

func TestParallelRace_FailoverArmsAreNotFlattenedIntoSingleRace(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			// First failover arm (parallel): should race only these two.
			"a": delayedBackend(120*time.Millisecond, completionEvents("first-arm-a")),
			"b": delayedBackend(140*time.Millisecond, completionEvents("first-arm-b")),
			// Second failover arm (parallel): must not participate unless first arm fully fails.
			"c": parallelBackend(completionEvents("second-arm-c")),
			"d": parallelBackend(completionEvents("second-arm-d")),
		},
		Rand: routing.NewSeededRng(1),
	}
	s, err := ex.Execute(context.Background(), parallelCall("a:model!b:model|c:model!d:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	got := col.Text.String()
	if got != "first-arm-a" && got != "first-arm-b" {
		t.Fatalf("winner text %q must come from first failover arm", got)
	}
}

func TestParallelRace_RecordsLoserAttemptLineage(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var loserOpened int32
	openGate := make(chan struct{})
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"winner": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					<-openGate
					return lipapi.NewFixedEventStream(completionEvents("winner")), nil
				},
			},
			"loser": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					atomic.AddInt32(&loserOpened, 1)
					return &cancelTrackingStream{
						events: completionEvents("loser"),
						delay:  2 * time.Second,
					}, nil
				},
			},
		},
		Rand: routing.NewSeededRng(1),
	}
	go func() {
		for {
			if atomic.LoadInt32(&loserOpened) > 0 {
				close(openGate)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "parallel-lineage"},
		Route:   lipapi.RouteIntent{Selector: "winner:model!loser:model"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	leg, err := st.FetchALeg(context.Background(), call.Session.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	atts, err := st.LoadAttempts(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) < 2 {
		t.Fatalf("expected at least 2 attempts (winner + loser), got %d", len(atts))
	}
	var sawSuccess, sawLoser bool
	for _, a := range atts {
		if a.Outcome == lipapi.AttemptSuccess {
			sawSuccess = true
		}
		if a.Outcome == lipapi.AttemptCancelled || a.Outcome == lipapi.AttemptSwallowedFailure {
			sawLoser = true
		}
	}
	if !sawSuccess || !sawLoser {
		t.Fatalf("expected success and loser attempt outcomes, got %#v", atts)
	}
}
