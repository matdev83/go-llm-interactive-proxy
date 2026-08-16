package runtimebundle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func newTestProcessOwner() (*processResourceOwner, *ProcessServices) {
	ps := &ProcessServices{}
	owner := &processResourceOwner{
		register: func(c func() error) {
			if c != nil {
				ps.closers = append(ps.closers, c)
			}
		},
	}
	return owner, ps
}

func TestProcessResourceOwner_OwnIgnoresNilRelease(t *testing.T) {
	t.Parallel()
	owner, ps := newTestProcessOwner()
	owner.Own(nil)
	if len(ps.closers) != 0 {
		t.Fatalf("Own(nil) registered %d closers, want 0", len(ps.closers))
	}
}

func TestProcessResourceOwner_OwnAppendsToAuthoritativeCloserSet(t *testing.T) {
	t.Parallel()
	owner, ps := newTestProcessOwner()
	var released atomic.Bool
	release := func() error {
		released.Store(true)
		return nil
	}
	owner.Own(release)
	if len(ps.closers) != 1 {
		t.Fatalf("closers len=%d, want 1", len(ps.closers))
	}
	// The owned release must be the same authoritative set consumed by Close.
	ps.closers[0]()
	if !released.Load() {
		t.Fatal("owned release was not the registered closure")
	}
}

func TestAcquireOwnedProcess_RegistersReleaseBeforeReturn(t *testing.T) {
	t.Parallel()
	owner, ps := newTestProcessOwner()
	var released atomic.Bool
	value, err := acquireOwnedProcess(context.Background(), owner, func(context.Context) (string, func() error, error) {
		return "resource", func() error {
			released.Store(true)
			return nil
		}, nil
	})
	if err != nil {
		t.Fatalf("acquireOwnedProcess: %v", err)
	}
	if value != "resource" {
		t.Fatalf("value=%q want %q", value, "resource")
	}
	// Release must already be registered in the authoritative closer set before
	// the value escaped to the caller.
	if len(ps.closers) != 1 {
		t.Fatalf("closers len=%d, want 1 (release must be registered before return)", len(ps.closers))
	}
	if released.Load() {
		t.Fatal("release must not have run during acquisition")
	}
}

func TestAcquireOwnedProcess_ErrorDoesNotRegister(t *testing.T) {
	t.Parallel()
	owner, ps := newTestProcessOwner()
	wantErr := errors.New("acquire boom")
	_, err := acquireOwnedProcess(context.Background(), owner, func(context.Context) (string, func() error, error) {
		return "", func() error { return nil }, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err=%v, want %v", err, wantErr)
	}
	if len(ps.closers) != 0 {
		t.Fatalf("acquisition error registered %d closers, want 0", len(ps.closers))
	}
}

func TestAcquireOwnedProcess_NilReleaseRejectsValue(t *testing.T) {
	t.Parallel()
	owner, ps := newTestProcessOwner()
	_, err := acquireOwnedProcess(context.Background(), owner, func(context.Context) (string, func() error, error) {
		// Ownership-contract violation: success with no release.
		return "leaky", nil, nil
	})
	if err == nil {
		t.Fatal("owned success with nil release must fail")
	}
	if len(ps.closers) != 0 {
		t.Fatalf("nil-release acquisition registered %d closers, want 0", len(ps.closers))
	}
}

func TestAcquireOwnedProcess_PropagatesAcquireError(t *testing.T) {
	t.Parallel()
	owner, _ := newTestProcessOwner()
	wantErr := errors.New("factory failed")
	_, err := acquireOwnedProcess(context.Background(), owner, func(context.Context) (int, func() error, error) {
		return 0, nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err=%v, want %v", err, wantErr)
	}
}

func TestProcessOwner_SharesCloserSetWithCloseExactlyOnce(t *testing.T) {
	t.Parallel()
	owner, ps := newTestProcessOwner()
	var mu sync.Mutex
	var order []string
	var closes atomic.Int32
	release := func(name string) func() error {
		return func() error {
			closes.Add(1)
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}
	owner.Own(release("a"))
	owner.Own(release("b"))
	// Normal shutdown consumes the authoritative set in reverse order, once.
	if err := ps.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("close order=%v, want [b a]", got)
	}
	if closes.Load() != 2 {
		t.Fatalf("closes=%d, want exactly 2", closes.Load())
	}
}

func TestProcessOwner_RollbackReleasesInReverseOrder(t *testing.T) {
	t.Parallel()
	owner, ps := newTestProcessOwner()
	var mu sync.Mutex
	var order []string
	release := func(name string) func() error {
		return func() error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}
	owner.Own(release("a"))
	owner.Own(release("b"))
	if err := withDisposedClosers(nil, ps.closers); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("rollback order=%v, want [b a]", got)
	}
}

func TestProcessOwner_AggregateCleanupErrors(t *testing.T) {
	t.Parallel()
	owner, ps := newTestProcessOwner()
	errA := errors.New("close-a")
	errB := errors.New("close-b")
	owner.Own(func() error { return errA })
	owner.Own(func() error { return errB })
	err := withDisposedClosers(nil, ps.closers)
	if err == nil {
		t.Fatal("expected aggregated cleanup error")
	}
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Fatalf("want both errors joined, got %v", err)
	}
}
