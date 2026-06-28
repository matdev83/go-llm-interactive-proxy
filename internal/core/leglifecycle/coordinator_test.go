package leglifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestCoordinator_CancelALeg_cancelsActiveBLegsBeforeClose(t *testing.T) {
	t.Parallel()
	c := NewCoordinator(CoordinatorConfig{CancelTimeout: time.Second})
	a := c.StartALeg("a-1")
	b1 := &recordingBLeg{}
	b2 := &recordingBLeg{}
	if err := a.RegisterBLeg(context.Background(), BLegHandle{ID: "b1", Attempt: b1}); err != nil {
		t.Fatal(err)
	}
	if err := a.RegisterBLeg(context.Background(), BLegHandle{ID: "b2", Attempt: b2}); err != nil {
		t.Fatal(err)
	}

	err := c.CancelALeg(context.Background(), "a-1", CancelCause{Kind: CancelClientGone})
	if err != nil {
		t.Fatal(err)
	}

	for name, b := range map[string]*recordingBLeg{"b1": b1, "b2": b2} {
		if got, want := b.calls(), []string{"cancel:client_gone", "close"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s calls = %v want %v", name, got, want)
		}
	}
}

func TestCoordinator_CancelALeg_blocksFutureBLegs(t *testing.T) {
	t.Parallel()
	c := NewCoordinator(CoordinatorConfig{})
	a := c.StartALeg("a-1")
	if err := c.CancelALeg(context.Background(), "a-1", CancelCause{Kind: CancelExplicit}); err != nil {
		t.Fatal(err)
	}

	err := a.RegisterBLeg(context.Background(), BLegHandle{ID: "late", Attempt: &recordingBLeg{}})
	if !errors.Is(err, ErrALegCanceled) {
		t.Fatalf("RegisterBLeg after cancel: got %v want ErrALegCanceled", err)
	}
	if err := a.Err(); !errors.Is(err, ErrALegCanceled) {
		t.Fatalf("a.Err: got %v want ErrALegCanceled", err)
	}
}

func TestCoordinator_CancelALeg_isIdempotentUnderConcurrency(t *testing.T) {
	t.Parallel()
	c := NewCoordinator(CoordinatorConfig{CancelTimeout: time.Second})
	a := c.StartALeg("a-1")
	b := &recordingBLeg{}
	if err := a.RegisterBLeg(context.Background(), BLegHandle{ID: "b1", Attempt: b}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			_ = c.CancelALeg(context.Background(), "a-1", CancelCause{Kind: CancelExplicit})
		})
	}
	wg.Wait()

	if got, want := b.calls(), []string{"cancel:explicit", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v want %v", got, want)
	}
}

func TestCoordinator_ZeroValueCoordinator_LazyMapInit(t *testing.T) {
	t.Parallel()
	var c Coordinator
	a := c.StartALeg("a-1")
	if a == nil {
		t.Fatal("expected StartALeg to return scope for zero-value coordinator")
	}
	b := &recordingBLeg{}
	if err := a.RegisterBLeg(context.Background(), BLegHandle{ID: "b1", Attempt: b}); err != nil {
		t.Fatal(err)
	}
	if err := c.CancelALeg(context.Background(), "a-1", CancelCause{Kind: CancelExplicit}); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinator_CancelALeg_PropagatesCancelAndCloseErrors(t *testing.T) {
	t.Parallel()
	c := NewCoordinator(CoordinatorConfig{CancelTimeout: time.Second})
	a := c.StartALeg("a-1")
	cancelErr := errors.New("cancel failed")
	closeErr := errors.New("close failed")
	if err := a.RegisterBLeg(context.Background(), BLegHandle{
		ID: "b1",
		Attempt: &erroringBLeg{
			cancelErr: cancelErr,
			closeErr:  closeErr,
		},
	}); err != nil {
		t.Fatal(err)
	}
	err := c.CancelALeg(context.Background(), "a-1", CancelCause{Kind: CancelExplicit})
	if err == nil {
		t.Fatal("expected cancel to surface cleanup errors")
	}
	if !errors.Is(err, cancelErr) {
		t.Fatalf("expected cancel error in aggregate, got %v", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected close error in aggregate, got %v", err)
	}
}

func TestCoordinator_RegisterBLegAfterCancel_PropagatesCleanupErrors(t *testing.T) {
	t.Parallel()
	c := NewCoordinator(CoordinatorConfig{CancelTimeout: time.Second})
	a := c.StartALeg("a-1")
	if err := c.CancelALeg(context.Background(), "a-1", CancelCause{Kind: CancelExplicit}); err != nil {
		t.Fatal(err)
	}
	lateCancelErr := errors.New("late cancel failed")
	lateCloseErr := errors.New("late close failed")
	err := a.RegisterBLeg(context.Background(), BLegHandle{
		ID: "late",
		Attempt: &erroringBLeg{
			cancelErr: lateCancelErr,
			closeErr:  lateCloseErr,
		},
	})
	if err == nil {
		t.Fatal("expected ErrALegCanceled with cleanup errors")
	}
	if !errors.Is(err, ErrALegCanceled) {
		t.Fatalf("expected ErrALegCanceled, got %v", err)
	}
	if !errors.Is(err, lateCancelErr) {
		t.Fatalf("expected late cancel error in aggregate, got %v", err)
	}
	if !errors.Is(err, lateCloseErr) {
		t.Fatalf("expected late close error in aggregate, got %v", err)
	}
}

func TestALeg_ReleaseBLeg_removesSingleLegFromCancelSweep(t *testing.T) {
	t.Parallel()
	c := NewCoordinator(CoordinatorConfig{CancelTimeout: time.Second})
	a := c.StartALeg("a-release")
	released := &recordingBLeg{}
	remaining := &recordingBLeg{}
	if err := a.RegisterBLeg(context.Background(), BLegHandle{ID: "b-released", Attempt: released}); err != nil {
		t.Fatal(err)
	}
	if err := a.RegisterBLeg(context.Background(), BLegHandle{ID: "b-remaining", Attempt: remaining}); err != nil {
		t.Fatal(err)
	}

	a.ReleaseBLeg("b-released")

	if got, want := released.calls(), []string(nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("released leg must not have been canceled before CancelALeg: got %v want %v", got, want)
	}
	if err := c.CancelALeg(context.Background(), "a-release", CancelCause{Kind: CancelExplicit}); err != nil {
		t.Fatal(err)
	}
	if got := released.calls(); got != nil {
		t.Fatalf("released leg must not be touched by CancelALeg, got %v", got)
	}
	if got, want := remaining.calls(), []string{"cancel:explicit", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("remaining leg must be canceled and closed: got %v want %v", got, want)
	}
}

func TestALeg_ReleaseBLeg_nilAndMissingAreNoOps(t *testing.T) {
	t.Parallel()
	c := NewCoordinator(CoordinatorConfig{})
	a := c.StartALeg("a-nil-release")
	b := &recordingBLeg{}
	if err := a.RegisterBLeg(context.Background(), BLegHandle{ID: "b1", Attempt: b}); err != nil {
		t.Fatal(err)
	}

	var nilALeg *ALeg
	nilALeg.ReleaseBLeg("anything")
	a.ReleaseBLeg("never-registered")
	a.ReleaseBLeg("")

	if err := c.CancelALeg(context.Background(), "a-nil-release", CancelCause{Kind: CancelExplicit}); err != nil {
		t.Fatal(err)
	}
	if got, want := b.calls(), []string{"cancel:explicit", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("b1 must still be canceled after no-op releases: got %v want %v", got, want)
	}
}

func TestALeg_ReleaseBLeg_isConcurrencySafe(t *testing.T) {
	t.Parallel()
	c := NewCoordinator(CoordinatorConfig{CancelTimeout: time.Second})
	a := c.StartALeg("a-race")
	const n = 32
	for i := range n {
		if err := a.RegisterBLeg(context.Background(), BLegHandle{ID: legID(i), Attempt: &recordingBLeg{}}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			a.ReleaseBLeg(legID(i))
		})
	}
	wg.Wait()
	if err := c.CancelALeg(context.Background(), "a-race", CancelCause{Kind: CancelExplicit}); err != nil {
		t.Fatalf("CancelALeg after concurrent releases: %v", err)
	}
}

func legID(i int) string {
	return fmt.Sprintf("b-%d", i)
}

type recordingBLeg struct {
	mu       sync.Mutex
	callsLog []string
}

func (r *recordingBLeg) Recv(context.Context) (lipapi.Event, error) {
	return lipapi.Event{}, io.EOF
}

func (r *recordingBLeg) Cancel(_ context.Context, cause CancelCause) CancelResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.callsLog = append(r.callsLog, "cancel:"+string(cause.Kind))
	return CancelResult{Mode: CancelModeProvider}
}

func (r *recordingBLeg) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.callsLog = append(r.callsLog, "close")
	return nil
}

func (r *recordingBLeg) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.callsLog...)
}

type erroringBLeg struct {
	cancelErr error
	closeErr  error
}

func (e *erroringBLeg) Recv(context.Context) (lipapi.Event, error) {
	return lipapi.Event{}, io.EOF
}

func (e *erroringBLeg) Cancel(context.Context, CancelCause) CancelResult {
	return CancelResult{Mode: CancelModeProvider, Err: e.cancelErr}
}

func (e *erroringBLeg) Close() error {
	return e.closeErr
}
