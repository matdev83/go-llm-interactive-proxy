package bedrock

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

type mockConverseReader struct {
	ch  <-chan types.ConverseStreamOutput
	err error
}

func (m *mockConverseReader) Events() <-chan types.ConverseStreamOutput { return m.ch }

func (m *mockConverseReader) Close() error { return nil }

func (m *mockConverseReader) Err() error { return m.err }

// closingChannelReader closes its event channel when Close is invoked (mirrors how the
// SDK invokes Reader.Close from ConverseStreamEventStream.Close).
type closingChannelReader struct {
	ch chan types.ConverseStreamOutput
}

func (m *closingChannelReader) Events() <-chan types.ConverseStreamOutput { return m.ch }

func (m *closingChannelReader) Close() error {
	close(m.ch)
	return nil
}

func (m *closingChannelReader) Err() error { return nil }

func TestConverseStream_Close_unblocksRecv(t *testing.T) {
	t.Parallel()
	recvAtSelect := make(chan struct{}, 1)
	recvSelectHookMu.Lock()
	recvSelectEntryHook = func() {
		select {
		case recvAtSelect <- struct{}{}:
		default:
		}
	}
	recvSelectHookMu.Unlock()
	t.Cleanup(func() {
		recvSelectHookMu.Lock()
		recvSelectEntryHook = nil
		recvSelectHookMu.Unlock()
	})

	ch := make(chan types.ConverseStreamOutput)
	sdk := bedrockruntime.NewConverseStreamEventStream(func(es *bedrockruntime.ConverseStreamEventStream) {
		es.Reader = &closingChannelReader{ch: ch}
	})
	es := newConverseStream(sdk, 0)
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		_, _ = es.Recv(context.Background())
	}()
	<-recvAtSelect
	if err := es.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	waitTimer := time.NewTimer(2 * time.Second)
	defer waitTimer.Stop()
	select {
	case <-recvDone:
	case <-waitTimer.C:
		t.Fatal("Recv did not unblock after Close")
	}
}

func TestConverseStream_Close_idempotent_race(t *testing.T) {
	t.Parallel()
	ch := make(chan types.ConverseStreamOutput)
	sdk := bedrockruntime.NewConverseStreamEventStream(func(es *bedrockruntime.ConverseStreamEventStream) {
		es.Reader = &closingChannelReader{ch: ch}
	})
	es := newConverseStream(sdk, 0)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			_ = es.Close()
		})
	}
	wg.Wait()
}

func TestConverseStream_Recv_wrapsSDKErr(t *testing.T) {
	t.Parallel()
	root := errors.New("root")
	ch := make(chan types.ConverseStreamOutput)
	close(ch)
	sdk := bedrockruntime.NewConverseStreamEventStream(func(es *bedrockruntime.ConverseStreamEventStream) {
		es.Reader = &mockConverseReader{ch: ch, err: root}
	})
	es := newConverseStream(sdk, 0)
	_, err := es.Recv(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "bedrock: recv stream") {
		t.Fatalf("got %q", err.Error())
	}
	if !errors.Is(err, root) {
		t.Fatalf("underlying: %v", err)
	}
}
