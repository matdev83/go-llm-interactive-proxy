package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	corerepair "github.com/matdev83/go-llm-interactive-proxy/internal/core/toolcallrepair"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

func TestRetryRecvStream_RealToolCallFinalizerSafeTailRepairs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		args       string
		schema     string
		want       string
		wantReason string
	}{
		{
			name:       "terminal_comma",
			args:       `{"location":"NYC",`,
			schema:     `{"type":"object","properties":{"location":{"type":"string"}},"required":["location"],"additionalProperties":false}`,
			want:       `{"location":"NYC"}`,
			wantReason: toolcall.ReasonSyntaxRepaired,
		},
		{
			name:       "pending_const",
			args:       `{"mode":`,
			schema:     `{"type":"object","properties":{"mode":{"const":"safe"}},"required":["mode"],"additionalProperties":false}`,
			want:       `{"mode":"safe"}`,
			wantReason: toolcall.ReasonConstInserted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			backendStream := lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventMessageStarted},
				{Kind: lipapi.EventToolCallStarted, ToolCallID: "safe-tail", ToolName: "run", MessageIndex: 3},
				{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "safe-tail", ToolName: "run", Delta: tc.args, MessageIndex: 3},
				{Kind: lipapi.EventToolCallFinished, ToolCallID: "safe-tail", ToolName: "run", MessageIndex: 3},
				{Kind: lipapi.EventResponseFinished},
			})
			var opens atomic.Int32
			ex, _ := policySecureExecutor(t, map[string]execbackend.Backend{
				"openai": recordingBackend("openai", &opens, backendStream),
			}, extensions.SnapshotOptions{RequestTransforms: []request.Transform{pdNoopRtx{}}})
			fin := corerepair.NewFinalizer(corerepair.FinalizerPolicy{
				ID:             corerepair.DefaultFinalizerID,
				MaxArgsBytes:   corerepair.DefaultMaxArgsBytes,
				OnUnrepairable: corerepair.OnUnrepairablePassThrough,
				Order:          corerepair.DefaultFinalizerOrder,
				Schema:         corerepair.DefaultSchemaLimits(),
			})
			ex.SetToolCallFinalizers([]toolcall.Finalizer{fin}, corerepair.DefaultMaxArgsBytes)

			call := pdBaseCall("openai:gpt-4")
			call.Tools = []lipapi.ToolDef{{Name: "run", Parameters: []byte(tc.schema)}}
			call.ToolChoice = lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}
			stream, err := ex.Execute(principalCtx("safe-tail-"+tc.name), call)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			got := tcrCollect(t, stream)
			_ = stream.Close()
			tools := tcrToolLifecycle(got)
			if len(tools) != 3 {
				t.Fatalf("tool lifecycle len=%d want 3: %#v", len(tools), tools)
			}
			if tools[1].Delta != tc.want || tools[0].MessageIndex != 3 || tools[2].MessageIndex != 3 {
				t.Fatalf("rewritten lifecycle=%#v want args=%q index=3", tools, tc.want)
			}

			res, err := fin.Finalize(context.Background(), toolcall.CompletedCall{
				ToolCallID: "safe-tail", ToolName: "run", ArgsJSON: []byte(tc.args),
			}, call.Tools[0], call.Tools, toolcall.Meta{})
			if err != nil {
				t.Fatal(err)
			}
			if res.ReasonCode != tc.wantReason {
				t.Fatalf("reason=%q want %q", res.ReasonCode, tc.wantReason)
			}
		})
	}
}
