package anthropic_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// FuzzWriteNonStreamJSON_toolArguments exercises json.Unmarshal on tool call arguments during encode.
func FuzzWriteNonStreamJSON_toolArguments(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"a":1}`))

	f.Fuzz(func(t *testing.T, args []byte) {
		args = testkit.CapBytes(args, 32<<10)
		call := &lipapi.Call{
			Route: lipapi.RouteIntent{Selector: "stub:claude"},
			Messages: []lipapi.Message{{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("hi")},
			}},
		}
		evs := []lipapi.Event{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventMessageStarted},
			{Kind: lipapi.EventToolCallStarted, ToolCallID: "idfuz", ToolName: "fn"},
			{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "idfuz", Delta: string(args)},
			{Kind: lipapi.EventToolCallFinished, ToolCallID: "idfuz"},
			{Kind: lipapi.EventResponseFinished},
		}
		rec := httptest.NewRecorder()
		_ = anthropic.WriteNonStreamJSON(context.Background(), rec, call, lipapi.NewFixedEventStream(evs), anthropic.EncodeOptions{})
	})
}
