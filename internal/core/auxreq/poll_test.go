//nolint:all
package auxreq_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

// poll helpers

func pollPendingRunner(start, release chan struct{}) backgroundRunner {
	return func(_ context.Context, _ *lipapi.Call) (lipapi.EventStream, error) {
		return &gatedStream{started: start, release: release}, nil
	}
}

func TestBackgroundScheduler_PollPendingWhenQueuedAndInFlight(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return pollPendingRunner(started, release)
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2, MaxResults: 4})

	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-pending"})
	require.NoError(t, err)

	// Poll immediately should be pending without blocking, even before worker starts.
	res, err := s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollPending, res.State)
	assert.Equal(t, 0, res.Collected.Text.Len())
	assert.Nil(t, res.Err)

	// Wait until worker has started (in-flight still pending).
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	res, err = s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollPending, res.State)

	// Complete and verify Poll becomes completed with defensive copy.
	close(release)
	// Await ensures completion.
	collected, err := s.Await(context.Background(), id)
	require.NoError(t, err)

	res, err = s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollCompleted, res.State)
	require.Equal(t, collected.Text.String(), res.Collected.Text.String())
	assert.Nil(t, res.Err)
}

func TestBackgroundScheduler_PollCompletedDefensiveCopy(t *testing.T) {
	t.Parallel()
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(_ context.Context, _ *lipapi.Call) (lipapi.EventStream, error) {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventMessageStarted},
				{Kind: lipapi.EventTextDelta, Delta: "hello"},
				{Kind: lipapi.EventToolCallStarted, ToolCallID: "call-1", ToolName: "tool-a"},
				{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call-1", Delta: `{"x":1}`},
				{Kind: lipapi.EventResponseFinished},
			}), nil
		})
	}, auxreq.SchedulerConfig{})

	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-copy"})
	require.NoError(t, err)
	_, err = s.Await(context.Background(), id)
	require.NoError(t, err)

	first, err := s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollCompleted, first.State)
	require.Equal(t, "hello", first.Collected.Text.String())

	// Mutate first without touching the stored builder's copyCheck path directly.
	// Replace Text with a fresh builder to avoid builder copy panic.
	first.Collected.Text = strings.Builder{}
	first.Collected.Text.WriteString("mutated")
	first.Collected.ToolNames["call-1"] = "mutated"
	if b := first.Collected.ToolArgs["call-1"]; b != nil {
		b.WriteString("-mutated")
	}
	first.Collected.ToolCallOrder[0] = "mutated"

	second, err := s.Poll(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "hello", second.Collected.Text.String(), "defensive copy must survive mutation")
	assert.Equal(t, "tool-a", second.Collected.ToolNames["call-1"])
	assert.Equal(t, `{"x":1}`, second.Collected.ToolArgs["call-1"].String())
	assert.Equal(t, []string{"call-1"}, second.Collected.ToolCallOrder)
}

func TestBackgroundScheduler_PollNotFoundAndEmptyID(t *testing.T) {
	t.Parallel()
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(_ context.Context, _ *lipapi.Call) (lipapi.EventStream, error) {
			return finishedStream(), nil
		})
	}, auxreq.SchedulerConfig{})

	res, err := s.Poll(context.Background(), "does-not-exist")
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollNotFound, res.State)
	assert.Equal(t, 0, res.Collected.Text.Len())
	assert.Nil(t, res.Err)

	_, err = s.Poll(context.Background(), "")
	require.ErrorIs(t, err, auxreq.ErrInvalidJobID)

	_, err = s.Poll(nil, "id")
	require.ErrorIs(t, err, lipapi.ErrNilContext)
}

func TestBackgroundScheduler_PollFailed(t *testing.T) {
	t.Parallel()
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(_ context.Context, _ *lipapi.Call) (lipapi.EventStream, error) {
			return nil, errors.New("boom")
		})
	}, auxreq.SchedulerConfig{})

	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-failed"})
	require.NoError(t, err)
	// Poll while pending should be pending, not failed yet.
	res, err := s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollPending, res.State)

	_, awaitErr := s.Await(context.Background(), id)
	require.Error(t, awaitErr)

	res, err = s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollFailed, res.State)
	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "boom")
	assert.Equal(t, 0, res.Collected.Text.Len(), "failed must return no content")
}

func TestBackgroundScheduler_PollDoesNotBlock(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return pollPendingRunner(started, release)
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 1})

	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-no-block"})
	require.NoError(t, err)
	<-started

	done := make(chan auxiliary.PollResult, 1)
	go func() {
		res, _ := s.Poll(context.Background(), id)
		done <- res
	}()

	select {
	case res := <-done:
		require.Equal(t, auxiliary.PollPending, res.State)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Poll blocked while job pending; must be non-blocking")
	}
	close(release)
	_, err = s.Await(context.Background(), id)
	require.NoError(t, err)
}

func TestBackgroundScheduler_PollAfterForgetBecomesNotFound(t *testing.T) {
	t.Parallel()
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(_ context.Context, _ *lipapi.Call) (lipapi.EventStream, error) {
			return finishedStream(), nil
		})
	}, auxreq.SchedulerConfig{})

	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-forget"})
	require.NoError(t, err)
	_, err = s.Await(context.Background(), id)
	require.NoError(t, err)

	res, err := s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollCompleted, res.State)

	s.Forget(id)

	res, err = s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollNotFound, res.State)
	assert.Equal(t, 0, res.Collected.Text.Len())
}

func TestBackgroundScheduler_PollAfterExpiryBecomesNotFound(t *testing.T) {
	t.Parallel()
	clock := newTestClock()
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(_ context.Context, _ *lipapi.Call) (lipapi.EventStream, error) {
			return finishedStream(), nil
		})
	}, auxreq.SchedulerConfig{ResultTTL: time.Minute, Now: clock.Now})

	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-ttl"})
	require.NoError(t, err)
	_, err = s.Await(context.Background(), id)
	require.NoError(t, err)

	// Poll before expiry is completed.
	res, err := s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollCompleted, res.State)

	clock.Advance(2 * time.Minute)

	// Poll after TTL must run cleanup and return not-found.
	res, err = s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollNotFound, res.State)
}

func TestBackgroundScheduler_PollAfterShutdown(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(ctx context.Context, _ *lipapi.Call) (lipapi.EventStream, error) {
			return &cancelAwareStream{started: started, canceled: make(chan struct{})}, nil
		})
	}, auxreq.SchedulerConfig{Workers: 1})

	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-shutdown"})
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}

	// Concurrent Poll and Close must be race-safe and never block.
	var wg sync.WaitGroup
	wg.Add(2)
	pollErr := make(chan error, 10)
	go func() {
		defer wg.Done()
		for range 20 {
			_, err := s.Poll(context.Background(), id)
			if err != nil {
				pollErr <- err
				return
			}
		}
		pollErr <- nil
	}()
	go func() {
		defer wg.Done()
		_ = s.Close()
	}()
	wg.Wait()
	close(pollErr)
	for err := range pollErr {
		if err != nil {
			// Cancellation of context should be reported as context error, not panic.
			assert.Error(t, err)
		}
	}
	// After shutdown, pending job may be failed (scheduler closed) or not-found if cleaned; both are non-blocking.
	res, err := s.Poll(context.Background(), id)
	if err != nil {
		// If scheduler already closed, Poll may return ErrSchedulerClosed for nil scheduler? Our scheduler still exists.
		require.Error(t, err)
	} else {
		assert.True(t, res.State == auxiliary.PollFailed || res.State == auxiliary.PollNotFound || res.State == auxiliary.PollPending, "state=%v", res.State)
	}
}

func TestBackgroundScheduler_PollRaceWithCompletion(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return pollPendingRunner(started, release)
	}, auxreq.SchedulerConfig{Workers: 1})

	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-race-completion"})
	require.NoError(t, err)
	<-started

	// Verify pending before race.
	res, err := s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollPending, res.State)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = s.Poll(context.Background(), id)
				time.Sleep(100 * time.Microsecond)
			}
		}
	})

	// Complete while concurrent polls race.
	time.Sleep(10 * time.Millisecond)
	close(release)
	_, err = s.Await(context.Background(), id)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	close(stop)
	wg.Wait()

	res, err = s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollCompleted, res.State)
}

func TestBackgroundScheduler_PollRaceWithForget(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(_ context.Context, _ *lipapi.Call) (lipapi.EventStream, error) {
			return &gatedStream{started: started, release: release}, nil
		})
	}, auxreq.SchedulerConfig{Workers: 1})

	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-race-forget"})
	require.NoError(t, err)
	<-started

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = s.Await(context.Background(), id)
	}()
	go func() {
		defer wg.Done()
		for range 20 {
			_, _ = s.Poll(context.Background(), id)
			s.Forget(id)
			_, _ = s.Poll(context.Background(), id)
		}
	}()
	close(release)
	wg.Wait()
	// After concurrent Forget, Poll must eventually be not-found and not panic.
	res, err := s.Poll(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, auxiliary.PollNotFound, res.State)
}

func TestBackgroundScheduler_PollDoesNotConsumeOrForget(t *testing.T) {
	t.Parallel()
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(_ context.Context, _ *lipapi.Call) (lipapi.EventStream, error) {
			return finishedStream(), nil
		})
	}, auxreq.SchedulerConfig{})

	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-no-consume"})
	require.NoError(t, err)
	_, err = s.Await(context.Background(), id)
	require.NoError(t, err)

	for range 3 {
		res, err := s.Poll(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, auxiliary.PollCompleted, res.State)
	}
	// Await should still succeed after multiple Polls.
	_, err = s.Await(context.Background(), id)
	require.NoError(t, err)
}

func TestBackgroundScheduler_PollPreservesAwaitSemantics(t *testing.T) {
	t.Parallel()
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(_ context.Context, _ *lipapi.Call) (lipapi.EventStream, error) {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventResponseFinished},
			}), nil
		})
	}, auxreq.SchedulerConfig{})

	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-await-preserve"})
	require.NoError(t, err)

	// Poll pending, then Await still blocks and succeeds.
	res, err := s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollPending, res.State)

	collected, err := s.Await(context.Background(), id)
	require.NoError(t, err)
	assert.True(t, collected.FinishReceived)

	// After Await, Poll still completed.
	res, err = s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollCompleted, res.State)

	// Forget after Await still works.
	s.Forget(id)
	_, err = s.Await(context.Background(), id)
	require.ErrorIs(t, err, auxreq.ErrJobNotFound)
	res, err = s.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollNotFound, res.State)
}

func TestBackgroundScheduler_PollWithCancelledContext(t *testing.T) {
	t.Parallel()
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(_ context.Context, _ *lipapi.Call) (lipapi.EventStream, error) {
			return finishedStream(), nil
		})
	}, auxreq.SchedulerConfig{})

	id, err := s.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-ctx"})
	require.NoError(t, err)
	_, err = s.Await(context.Background(), id)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.Poll(ctx, id)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

func TestBackgroundScheduler_PollViaBoundClient(t *testing.T) {
	t.Parallel()
	s := newBackground(context.Background(), t, func() auxreq.ExecutorRunner {
		return backgroundRunner(func(_ context.Context, _ *lipapi.Call) (lipapi.EventStream, error) {
			return finishedStream(), nil
		})
	}, auxreq.SchedulerConfig{})

	client := s.BindRunner(backgroundRunner(func(_ context.Context, _ *lipapi.Call) (lipapi.EventStream, error) {
		return finishedStream(), nil
	}))

	id, err := client.SubmitCollect(context.Background(), backgroundRequest(), auxiliary.SubmitOptions{CoalesceKey: "poll-bound"})
	require.NoError(t, err)

	// Bound client Poll should delegate to scheduler.
	poller, ok := any(client).(auxiliary.BackgroundPoller)
	require.True(t, ok, "bound client should implement BackgroundPoller")

	res, err := poller.Poll(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, auxiliary.PollPending, res.State)

	_, err = client.Await(context.Background(), id)
	require.NoError(t, err)

	res, err = poller.Poll(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, auxiliary.PollCompleted, res.State)
}

func TestBackgroundScheduler_SatisfiesBothInterfaces(t *testing.T) {
	t.Parallel()
	var _ auxiliary.BackgroundClient = (*auxreq.BackgroundScheduler)(nil)
	var _ auxiliary.BackgroundPoller = (*auxreq.BackgroundScheduler)(nil)
	var s auxreq.BackgroundScheduler
	_, isClient := any(&s).(auxiliary.BackgroundClient)
	_, isPoller := any(&s).(auxiliary.BackgroundPoller)
	require.True(t, isClient)
	require.True(t, isPoller)
}
