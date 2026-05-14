package leglifecycle

import (
	"context"
	"errors"
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.CancelALeg(context.Background(), "a-1", CancelCause{Kind: CancelExplicit})
		}()
	}
	wg.Wait()

	if got, want := b.calls(), []string{"cancel:explicit", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v want %v", got, want)
	}
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
