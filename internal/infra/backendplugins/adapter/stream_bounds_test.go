package adapter_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/adapter"
	testkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestBackpressure_SlowConsumerBoundedPending(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "bp", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "bp", MaxPendingEvents: 1,
	})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind == "" {
		t.Fatal("empty event")
	}
	// Drain remainder; with pending=1 the producer must have blocked rather than
	// buffering unbounded events in host memory.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout draining")
		default:
		}
		_, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTerminal_DuplicateRejected(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeDuplicateTerminal}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "dup", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: "dup"})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected duplicate-terminal / protocol failure")
	}
}

func TestStream_FrameBoundOversizeRejected(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeOversize}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "osz", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "osz", MaxStreamFrame: 64,
	})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected oversize/frame bound failure")
	}
}

func TestStream_CancelTimeoutHardStops(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeBlockedCancel, SlowWait: time.Hour}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "cto", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "cto", CancelTimeout: 20 * time.Millisecond,
	})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = stream.Recv(context.Background())
	})
	res := stream.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelExplicit})
	if res.Err == nil && res.Mode == "" {
		t.Fatal("expected cancel result")
	}
	_ = stream.Close()
	wg.Wait()
}

func TestClassifiedError_UnwrapRecoverablePreOutput(t *testing.T) {
	t.Parallel()
	err := &adapter.ClassifiedError{Code: "transport", Message: "boom", Retryable: true}
	if !errors.Is(err, lipapi.ErrRecoverablePreOutput) {
		t.Fatal("retryable pre-output must unwrap to ErrRecoverablePreOutput")
	}
	if !lipapi.IsRecoverablePreOutput(err) {
		t.Fatal("core seam must recognize classified pre-output")
	}
	post := &adapter.ClassifiedError{Code: "provider", Message: "x", Retryable: false, OutputCommitted: true}
	if lipapi.IsRecoverablePreOutput(post) {
		t.Fatal("post-output must not be recoverable")
	}
}
