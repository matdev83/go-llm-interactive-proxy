package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWithAccountTxRetriesBusyThenSucceeds(t *testing.T) {
	t.Parallel()
	var calls int
	out, err := withAccountTx(context.Background(), accountTxRetry{Attempts: 4, Delay: time.Millisecond}, func() (int, error) {
		calls++
		if calls < 3 {
			return 0, errors.New("database is locked")
		}
		return 7, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != 7 || calls != 3 {
		t.Fatalf("out=%d calls=%d, want 7/3", out, calls)
	}
}

func TestWithAccountTxClassifiesNonRetryable(t *testing.T) {
	t.Parallel()
	permanent := errors.New("account invalid")
	classified := errors.New("classified")
	var calls int
	_, err := withAccountTx(context.Background(), accountTxRetry{
		Attempts: 4, Delay: time.Millisecond,
		Classify: func(error) error { return classified },
	}, func() (int, error) {
		calls++
		return 0, permanent
	})
	if !errors.Is(err, classified) {
		t.Fatalf("err=%v, want classified", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}
}

func TestWithAccountTxExhaustedReturnsConfiguredError(t *testing.T) {
	t.Parallel()
	exhausted := errors.New("budget exhausted")
	var calls int
	_, err := withAccountTx(context.Background(), accountTxRetry{
		Attempts: 3, Delay: time.Millisecond, Exhausted: exhausted,
	}, func() (int, error) {
		calls++
		return 0, errors.New("database is locked")
	})
	if !errors.Is(err, exhausted) {
		t.Fatalf("err=%v, want exhausted", err)
	}
	if calls != 3 {
		t.Fatalf("calls=%d, want 3", calls)
	}
}

func TestWithAccountTxHonorsContextCancelDuringWait(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	var calls int
	_, err := withAccountTx(ctx, accountTxRetry{Attempts: 5, Delay: 50 * time.Millisecond}, func() (int, error) {
		calls++
		cancel()
		return 0, errors.New("database is locked")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}
}

func TestWithAccountTxErrReturnsLastRetryableError(t *testing.T) {
	t.Parallel()
	last := errors.New("database is locked")
	err := withAccountTxErr(context.Background(), accountTxRetry{Attempts: 2, Delay: time.Millisecond}, func() error {
		return last
	})
	if !errors.Is(err, last) {
		t.Fatalf("err=%v, want last retryable", err)
	}
}
