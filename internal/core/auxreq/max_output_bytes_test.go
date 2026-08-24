package auxreq_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/stretchr/testify/require"
)

// countingStream is a deterministic finite counting stream for TDD.
type countingStream struct {
	events []lipapi.Event
	pos    int
	recvs  atomic.Int32
}

func newCountingStream(events []lipapi.Event) *countingStream {
	cp := make([]lipapi.Event, len(events))
	copy(cp, events)
	return &countingStream{events: cp}
}

func (c *countingStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if c == nil {
		return lipapi.Event{}, lipapi.ErrNilFixedEventStream
	}
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return lipapi.Event{}, err
	}
	c.recvs.Add(1)
	if c.pos >= len(c.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := c.events[c.pos]
	c.pos++
	return ev, nil
}

func (c *countingStream) Close() error     { return nil }
func (c *countingStream) RecvCount() int32 { return c.recvs.Load() }

type countingRunner struct {
	events []lipapi.Event
	stream *countingStream
}

func (r *countingRunner) Execute(ctx context.Context, _ *lipapi.Call) (lipapi.EventStream, error) {
	cs := newCountingStream(r.events)
	r.stream = cs
	return cs, nil
}

func chunk(n int) string { return strings.Repeat("a", n) }

func textEvents(chunkSize, chunks int) []lipapi.Event {
	evs := []lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventMessageStarted}}
	for i := 0; i < chunks; i++ {
		evs = append(evs, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: chunk(chunkSize)})
	}
	evs = append(evs, lipapi.Event{Kind: lipapi.EventResponseFinished})
	return evs
}

func reasoningEvents(chunkSize, chunks int) []lipapi.Event {
	evs := []lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventMessageStarted}}
	for i := 0; i < chunks; i++ {
		evs = append(evs, lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: chunk(chunkSize)})
	}
	evs = append(evs, lipapi.Event{Kind: lipapi.EventResponseFinished})
	return evs
}

func toolArgsEvents(chunkSize, chunks int) []lipapi.Event {
	evs := []lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventMessageStarted}}
	for i := 0; i < chunks; i++ {
		evs = append(evs, lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call-1", Delta: chunk(chunkSize)})
	}
	evs = append(evs, lipapi.Event{Kind: lipapi.EventResponseFinished})
	return evs
}

func awaitResultTooLarge(t *testing.T, s *auxreq.BackgroundScheduler, id auxiliary.JobID) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := s.Await(ctx, id)
	return err
}

func pollResultTooLarge(t *testing.T, s *auxreq.BackgroundScheduler, id auxiliary.JobID) (auxiliary.PollResult, error) {
	t.Helper()
	// Await above synchronizes completion; Poll is non-blocking and terminal now.
	return s.Poll(context.Background(), id)
}

func TestBackgroundScheduler_MaxOutputBytes_TextStopsEarlyNo64MiBAccumulation(t *testing.T) {
	t.Parallel()
	// 1MiB chunks, effective 512KiB => first delta exceeds, should stop after 3 Recvs (started, message, delta).
	cr := &countingRunner{events: textEvents(1<<20, 64)}
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner { return cr }, auxreq.SchedulerConfig{MaxResultBytes: 8 << 20})
	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "maxout-text", MaxOutputBytes: 512 << 10})
	require.NoError(t, err)
	require.NotEmpty(t, id)

	awaitErr := awaitResultTooLarge(t, s, id)
	require.Error(t, awaitErr)
	require.ErrorIs(t, awaitErr, auxiliary.ErrResultTooLarge)
	require.ErrorIs(t, awaitErr, auxreq.ErrResultTooLarge)
	require.ErrorIs(t, awaitErr, lipapi.ErrCollectLimitExceeded)

	pr, err := pollResultTooLarge(t, s, id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollFailed, pr.State)
	require.ErrorIs(t, pr.Err, auxiliary.ErrResultTooLarge)
	require.ErrorIs(t, pr.Err, auxreq.ErrResultTooLarge)
	require.ErrorIs(t, pr.Err, lipapi.ErrCollectLimitExceeded)

	// Ensure no 64MiB accumulation: only a few Recvs, not 64.
	rc := cr.stream.RecvCount()
	if rc > 5 {
		t.Fatalf("RecvCount=%d want <=5 (early stop, no 64MiB accumulation)", rc)
	}
	if rc < 3 {
		t.Fatalf("RecvCount=%d want >=3", rc)
	}
}

func TestBackgroundScheduler_MaxOutputBytes_ReasoningDeltaBounded(t *testing.T) {
	t.Parallel()
	// Small chunks 256KiB, effective 512KiB => 3rd delta exceeds (256*2=512, third would be 768).
	cr := &countingRunner{events: reasoningEvents(256<<10, 10)}
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner { return cr }, auxreq.SchedulerConfig{MaxResultBytes: 8 << 20})
	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "maxout-reasoning", MaxOutputBytes: 512 << 10})
	require.NoError(t, err)
	awaitErr := awaitResultTooLarge(t, s, id)
	require.Error(t, awaitErr)
	require.ErrorIs(t, awaitErr, auxiliary.ErrResultTooLarge)
	require.ErrorIs(t, awaitErr, auxreq.ErrResultTooLarge)
	require.ErrorIs(t, awaitErr, lipapi.ErrCollectLimitExceeded)
	rc := cr.stream.RecvCount()
	// Started + message + 2 successful deltas + 1 failing delta = 5
	if rc > 6 {
		t.Fatalf("reasoning RecvCount=%d want <=6", rc)
	}
}

func TestBackgroundScheduler_MaxOutputBytes_ToolArgsBounded(t *testing.T) {
	t.Parallel()
	cr := &countingRunner{events: toolArgsEvents(128<<10, 10)}
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner { return cr }, auxreq.SchedulerConfig{MaxResultBytes: 8 << 20})
	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "maxout-toolargs", MaxOutputBytes: 512 << 10})
	require.NoError(t, err)
	awaitErr := awaitResultTooLarge(t, s, id)
	require.Error(t, awaitErr)
	require.ErrorIs(t, awaitErr, auxiliary.ErrResultTooLarge)
	require.ErrorIs(t, awaitErr, auxreq.ErrResultTooLarge)
	require.ErrorIs(t, awaitErr, lipapi.ErrCollectLimitExceeded)
	rc := cr.stream.RecvCount()
	if rc > 7 {
		t.Fatalf("toolArgs RecvCount=%d want <=7", rc)
	}
}

func TestBackgroundScheduler_MaxOutputBytes_ZeroAllowsBelowSchedulerCap(t *testing.T) {
	t.Parallel()
	// Scheduler cap 1MiB, per-job 0 (inherit), output 512KiB should succeed.
	cr := &countingRunner{events: textEvents(512<<10, 1)}
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner { return cr }, auxreq.SchedulerConfig{MaxResultBytes: 1 << 20})
	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "maxout-zero-allows", MaxOutputBytes: 0})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	col, err := s.Await(ctx, id)
	require.NoError(t, err)
	require.Equal(t, 512<<10, col.Text.Len())
	pr, err := s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollCompleted, pr.State)
}

func TestBackgroundScheduler_MaxOutputBytes_LargerClampedByScheduler(t *testing.T) {
	t.Parallel()
	// Scheduler 512KiB, per-job 8MiB => effective 512KiB. Output 600KiB should fail (clamped).
	text600k := chunk(600 << 10)
	evs := []lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventMessageStarted}, {Kind: lipapi.EventTextDelta, Delta: text600k}, {Kind: lipapi.EventResponseFinished}}
	cr := &countingRunner{events: evs}
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner { return cr }, auxreq.SchedulerConfig{MaxResultBytes: 512 << 10})
	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "maxout-clamped", MaxOutputBytes: 8 << 20})
	require.NoError(t, err)
	awaitErr := awaitResultTooLarge(t, s, id)
	require.Error(t, awaitErr)
	require.ErrorIs(t, awaitErr, auxiliary.ErrResultTooLarge)
	require.ErrorIs(t, awaitErr, auxreq.ErrResultTooLarge)
	// This path is still CollectWithLimits, so lipapi sentinel also present.
	require.ErrorIs(t, awaitErr, lipapi.ErrCollectLimitExceeded)
}

func TestBackgroundScheduler_MaxOutputBytes_AggregateMixTriggersPostEstimate(t *testing.T) {
	t.Parallel()
	// Each dimension below effective but combined above => post-estimate >effective.
	// Text 300KiB + Reasoning 300KiB = 600KiB > 512KiB effective.
	// ReasoningDelta check does NOT include Text length, so CollectWithLimits will pass,
	// but estimateCollectedBytes will catch combined.
	evs := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: chunk(300 << 10)},
		{Kind: lipapi.EventReasoningDelta, Delta: chunk(300 << 10)},
		{Kind: lipapi.EventResponseFinished},
	}
	cr := &countingRunner{events: evs}
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner { return cr }, auxreq.SchedulerConfig{MaxResultBytes: 8 << 20})
	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "maxout-aggregate", MaxOutputBytes: 512 << 10})
	require.NoError(t, err)
	awaitErr := awaitResultTooLarge(t, s, id)
	require.Error(t, awaitErr)
	require.ErrorIs(t, awaitErr, auxiliary.ErrResultTooLarge)
	require.ErrorIs(t, awaitErr, auxreq.ErrResultTooLarge)
	// Post-estimate path does NOT wrap lipapi.ErrCollectLimitExceeded.
	if errors.Is(awaitErr, lipapi.ErrCollectLimitExceeded) {
		t.Fatalf("aggregate post-estimate should NOT be ErrCollectLimitExceeded, got %v", awaitErr)
	}
	pr, err := pollResultTooLarge(t, s, id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollFailed, pr.State)
	require.ErrorIs(t, pr.Err, auxiliary.ErrResultTooLarge)
}

func TestBackgroundScheduler_MaxOutputBytes_TerminalErrorNotResultTooLargeWhenUnderCap(t *testing.T) {
	t.Parallel()
	evs := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "small"},
		{Kind: lipapi.EventError, ErrorCode: "upstream", ErrorMessage: "boom"},
	}
	cr := &countingRunner{events: evs}
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner { return cr }, auxreq.SchedulerConfig{MaxResultBytes: 512 << 10})
	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "maxout-terminal", MaxOutputBytes: 512 << 10})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, awaitErr := s.Await(ctx, id)
	require.Error(t, awaitErr)
	if errors.Is(awaitErr, auxiliary.ErrResultTooLarge) || errors.Is(awaitErr, auxreq.ErrResultTooLarge) {
		t.Fatalf("terminal error must NOT be ResultTooLarge, got %v", awaitErr)
	}
	var se *lipapi.StreamError
	if !errors.As(awaitErr, &se) {
		t.Fatalf("want *lipapi.StreamError, got %T: %v", awaitErr, awaitErr)
	}
	if se.Code != "upstream" {
		t.Fatalf("code=%q want upstream", se.Code)
	}
	pr, err := pollResultTooLarge(t, s, id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollFailed, pr.State)
	if errors.Is(pr.Err, auxiliary.ErrResultTooLarge) {
		t.Fatalf("poll terminal error must NOT be ResultTooLarge")
	}
}

func TestBackgroundScheduler_MaxOutputBytes_WarningsAndMediaRetainDefaultBounds(t *testing.T) {
	t.Parallel()
	// With small effective (512KiB), warnings and media should still use DefaultCollectLimits (100k warnings, 1024 media).
	// This stream has small text + 2 warnings + 1 media, well under default, should succeed.
	evs := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "hi"},
		{Kind: lipapi.EventWarning, WarningMessage: "w1"},
		{Kind: lipapi.EventWarning, WarningMessage: "w2"},
		{Kind: lipapi.EventAssistantImageRef, AssistantRef: "https://example.com/x.png", AssistantMIME: "image/png"},
		{Kind: lipapi.EventResponseFinished},
	}
	cr := &countingRunner{events: evs}
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner { return cr }, auxreq.SchedulerConfig{MaxResultBytes: 8 << 20})
	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "maxout-warn-media", MaxOutputBytes: 512 << 10})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	col, err := s.Await(ctx, id)
	require.NoError(t, err)
	require.Len(t, col.Warnings, 2)
	require.Len(t, col.AssistantMedia, 1)
}

func TestBackgroundScheduler_MaxOutputBytes_SpyNoFullOversizedCollection(t *testing.T) {
	t.Parallel()
	// Spy ensures we don't collect full 64MiB when limit is 512KiB.
	// Use 64 chunks of 1MiB; effective 512KiB should stop at first chunk.
	cr := &countingRunner{events: textEvents(1<<20, 64)}
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner { return cr }, auxreq.SchedulerConfig{MaxResultBytes: 8 << 20})
	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "maxout-spy", MaxOutputBytes: 512 << 10})
	require.NoError(t, err)
	awaitErr := awaitResultTooLarge(t, s, id)
	require.Error(t, awaitErr)
	// Spy: ensure we never needed to buffer near 64MiB; RecvCount proves early stop.
	if rc := cr.stream.RecvCount(); rc > 5 {
		t.Fatalf("spy RecvCount=%d want early stop (no 64MiB collection)", rc)
	}
	// Poll also reflects same error without extra collection.
	pr, err := s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollFailed, pr.State)
}

func BenchmarkBackgroundScheduler_MaxOutputBytes_TextLimit(b *testing.B) {
	evs := textEvents(1<<20, 4)
	cr := &countingRunner{events: evs}
	s, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner { return cr }, auxreq.SchedulerConfig{MaxResultBytes: 8 << 20})
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id, _ := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "bench-" + chunk(4)[0:1] + string(rune(i)), MaxOutputBytes: 512 << 10})
		_, _ = s.Await(context.Background(), id)
	}
}
