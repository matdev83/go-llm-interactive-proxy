package stopguardverify_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguardverify"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdapter_Observer_SuccessPopulatesUsageAndLatency asserts successful Collect
// populates usage from lipapi.Collected and Latency > 0.
func TestAdapter_Observer_SuccessPopulatesUsageAndLatency(t *testing.T) {
	t.Parallel()
	var obs stopguardverify.VerifyObservation
	var called int
	cfg := stopguardverify.AdapterConfig{
		Role:    "loop_guard",
		Timeout: 4 * time.Second,
		Observer: func(o stopguardverify.VerifyObservation) {
			obs = o
			called++
		},
	}
	collected := collectedWithText(`{"kind":"allow_stop","reason":"done"}`)
	collected.InputTokens = 11
	collected.OutputTokens = 22
	collected.TotalTokens = 33
	collected.CostNanoUnits = 99
	fake := &fakeAuxClient{collected: collected}
	// also set via direct struct to ensure fields propagate
	fake.collected.InputTokens = 11
	fake.collected.OutputTokens = 22
	fake.collected.TotalTokens = 33
	fake.collected.CostNanoUnits = 99
	adapter := stopguardverify.NewAdapter(fake, cfg)
	_, err := adapter.Verify(context.Background(), stopguard.Evidence{Cause: stopguard.CauseNormalEnd})
	require.NoError(t, err)
	require.Equal(t, 1, called, "observer must be called exactly once")
	assert.GreaterOrEqual(t, obs.Latency, time.Duration(0))
	assert.Equal(t, 11, obs.InputTokens)
	assert.Equal(t, 22, obs.OutputTokens)
	assert.Equal(t, 33, obs.TotalTokens)
	assert.Equal(t, int64(99), obs.CostNanoUnits)
	assert.NoError(t, obs.Err)
	_ = lipapi.Collected{} // ensure import used
}

// TestAdapter_Observer_ErrorPathReportsErrAndZeroUsage asserts error path calls
// Observer with Err set and zero usage.
func TestAdapter_Observer_ErrorPathReportsErrAndZeroUsage(t *testing.T) {
	t.Parallel()
	var obs stopguardverify.VerifyObservation
	var called int
	cfg := stopguardverify.AdapterConfig{
		Role:    "loop_guard",
		Timeout: 4 * time.Second,
		Observer: func(o stopguardverify.VerifyObservation) {
			obs = o
			called++
		},
	}
	fake := &fakeAuxClient{err: errors.New("transport failure")}
	adapter := stopguardverify.NewAdapter(fake, cfg)
	verdict, err := adapter.Verify(context.Background(), stopguard.Evidence{Cause: stopguard.CauseNormalEnd})
	require.Error(t, err)
	assert.Equal(t, stopguard.VerdictUncertain, verdict.Kind)
	require.Equal(t, 1, called)
	assert.Error(t, obs.Err)
	assert.Equal(t, 0, obs.InputTokens)
	assert.Equal(t, 0, obs.OutputTokens)
	assert.Equal(t, 0, obs.TotalTokens)
	assert.Equal(t, int64(0), obs.CostNanoUnits)
}

// TestAdapter_Observer_NilObserverDoesNotPanic asserts nil Observer must not panic.
func TestAdapter_Observer_NilObserverDoesNotPanic(t *testing.T) {
	t.Parallel()
	cfg := stopguardverify.AdapterConfig{Role: "loop_guard", Timeout: 4 * time.Second}
	// Observer is nil by default
	fake := &fakeAuxClient{collected: collectedWithText(`{"kind":"allow_stop","reason":"ok"}`)}
	adapter := stopguardverify.NewAdapter(fake, cfg)
	assert.NotPanics(t, func() {
		_, err := adapter.Verify(context.Background(), stopguard.Evidence{Cause: stopguard.CauseNormalEnd})
		require.NoError(t, err)
	})
	// also error path with nil observer must not panic
	fakeErr := &fakeAuxClient{err: errors.New("boom")}
	adapter2 := stopguardverify.NewAdapter(fakeErr, cfg)
	assert.NotPanics(t, func() {
		_, _ = adapter2.Verify(context.Background(), stopguard.Evidence{Cause: stopguard.CauseNormalEnd})
	})
}

// TestAdapter_Observer_CancelledContextReports asserts ctx already-cancelled path also reports.
func TestAdapter_Observer_CancelledContextReports(t *testing.T) {
	t.Parallel()
	var obs stopguardverify.VerifyObservation
	var called int
	cfg := stopguardverify.AdapterConfig{
		Role:    "loop_guard",
		Timeout: 4 * time.Second,
		Observer: func(o stopguardverify.VerifyObservation) {
			obs = o
			called++
		},
	}
	fake := &fakeAuxClient{collected: collectedWithText(`{"kind":"allow_stop","reason":"ok"}`)}
	adapter := stopguardverify.NewAdapter(fake, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	verdict, err := adapter.Verify(ctx, stopguard.Evidence{Cause: stopguard.CauseNormalEnd})
	require.Error(t, err)
	assert.Equal(t, stopguard.VerdictUncertain, verdict.Kind)
	require.Equal(t, 1, called, "observer must be called even when ctx already cancelled")
	assert.Error(t, obs.Err)
	assert.ErrorIs(t, obs.Err, context.Canceled)
	assert.Equal(t, 0, fake.calls, "client must not be called on cancelled ctx")
}
