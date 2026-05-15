package decodeqos

import (
	"context"
	"errors"
	"testing"
)

func TestNewLimiter_unlimitedReturnsNil(t *testing.T) {
	t.Parallel()

	for _, max := range []int{0, -1} {
		t.Run("max", func(t *testing.T) {
			t.Parallel()
			if got := NewLimiter(max); got != nil {
				t.Fatalf("NewLimiter(%d) = %#v, want nil", max, got)
			}
		})
	}
}

func TestLimiter_TryAcquire_nilLimiterIsNoop(t *testing.T) {
	t.Parallel()

	var l *Limiter
	release, ok, err := l.TryAcquire(context.Background())
	if err != nil || !ok {
		t.Fatalf("TryAcquire nil limiter = release:%v ok:%v err:%v, want ok", release != nil, ok, err)
	}
	release()
}

func TestLimiter_TryAcquire_capacityOneFailsFastUntilRelease(t *testing.T) {
	t.Parallel()

	l := NewLimiter(1)
	release, ok, err := l.TryAcquire(context.Background())
	if err != nil || !ok {
		t.Fatalf("first TryAcquire = ok:%v err:%v, want ok", ok, err)
	}

	_, ok, err = l.TryAcquire(context.Background())
	if err != nil || ok {
		t.Fatalf("second TryAcquire while saturated = ok:%v err:%v, want saturated without error", ok, err)
	}

	release()
	release, ok, err = l.TryAcquire(context.Background())
	if err != nil || !ok {
		t.Fatalf("TryAcquire after release = ok:%v err:%v, want ok", ok, err)
	}
	release()
}

func TestLimiter_TryAcquire_canceledContextReturnsPromptly(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	l := NewLimiter(1)
	release, ok, err := l.TryAcquire(ctx)
	if release != nil || ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("TryAcquire canceled context = release:%v ok:%v err:%v, want context canceled", release != nil, ok, err)
	}
}
