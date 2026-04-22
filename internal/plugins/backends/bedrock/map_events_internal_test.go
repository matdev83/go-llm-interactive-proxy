package bedrock

import (
	"context"
	"errors"
	"strings"
	"testing"

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

func TestConverseStream_Recv_wrapsSDKErr(t *testing.T) {
	t.Parallel()
	root := errors.New("root")
	ch := make(chan types.ConverseStreamOutput)
	close(ch)
	sdk := bedrockruntime.NewConverseStreamEventStream(func(es *bedrockruntime.ConverseStreamEventStream) {
		es.Reader = &mockConverseReader{ch: ch, err: root}
	})
	es := newConverseStream(sdk)
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
