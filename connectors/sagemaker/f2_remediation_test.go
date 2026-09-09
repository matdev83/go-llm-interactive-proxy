package sagemaker_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sagemaker"
	"github.com/aws/aws-sdk-go-v2/service/sagemakerruntime"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/sagemaker/internal/service"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

// F2 remediation: ListModels must expose only the configured endpoint and
// inference must collect the unary response (no provider event-stream
// buffering, no provider streaming advertised).

type f2StubRuntime struct {
	calls    int
	invokeFn func(context.Context, *sagemakerruntime.InvokeEndpointInput) (*sagemakerruntime.InvokeEndpointOutput, error)
}

func (f *f2StubRuntime) InvokeEndpoint(ctx context.Context, params *sagemakerruntime.InvokeEndpointInput, _ ...func(*sagemakerruntime.Options)) (*sagemakerruntime.InvokeEndpointOutput, error) {
	f.calls++
	if f.invokeFn != nil {
		return f.invokeFn(ctx, params)
	}
	return &sagemakerruntime.InvokeEndpointOutput{Body: []byte(`{"generated_text":"ok"}`)}, nil
}

type f2StubControl struct {
	calls int
}

func (f *f2StubControl) ListEndpoints(context.Context, *sagemaker.ListEndpointsInput, ...func(*sagemaker.Options)) (*sagemaker.ListEndpointsOutput, error) {
	f.calls++
	return nil, errors.New("sagemaker: control plane must not be consulted for inventory")
}

func f2Configure(t *testing.T, r *f2StubRuntime, c *f2StubControl, endpoint string) backendplugin.ConfiguredInstance {
	t.Helper()
	svc := service.New(service.WithStaticClients(r, c))
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "f2-inst",
		ConfigYAML:  []byte("region: us-east-1\nendpoint_name: " + endpoint + "\ninference_contract: hf-text-generation\n"),
		Secrets:     testStaticSecrets(),
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}
	return inst
}

// ListModels with control plane advertising A/B/C (here: control fails if
// consulted at all) still advertises exactly sagemaker/A without streaming.
func TestF2_ListModels_ExposesOnlyConfiguredEndpoint(t *testing.T) {
	t.Parallel()
	r := &f2StubRuntime{}
	c := &f2StubControl{}
	inst := f2Configure(t, r, c, "ep-a")

	resp, err := inst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("expected exactly 1 model, got %d: %+v", len(resp.Models), resp.Models)
	}
	if resp.Models[0].CanonicalModelID != "sagemaker/ep-a" {
		t.Fatalf("CanonicalModelID=%q want %q", resp.Models[0].CanonicalModelID, "sagemaker/ep-a")
	}
	if resp.Models[0].NativeModelID != "ep-a" {
		t.Fatalf("NativeModelID=%q want %q", resp.Models[0].NativeModelID, "ep-a")
	}
	if resp.Models[0].Capabilities.Streaming {
		t.Fatalf("provider streaming must not be advertised in ListModels")
	}
	if c.calls != 0 {
		t.Fatalf("ListModels consulted control plane %d times; must expose only the configured endpoint", c.calls)
	}
}

// Execute(B) while configured for A fails closed with an explicit
// endpoint_name mismatch error and never invokes the runtime.
func TestF2_Execute_UnconfiguredEndpoint_FailsClosed(t *testing.T) {
	t.Parallel()
	r := &f2StubRuntime{}
	c := &f2StubControl{}
	inst := f2Configure(t, r, c, "ep-a")

	stream := newTestExecuteStream(context.Background(), "sagemaker/ep-b", lipapi.OperationOpenAIChatCompletions, "hello", true, nil)
	err := inst.Execute(stream)
	if err == nil {
		t.Fatalf("expected Execute(sagemaker/ep-b) to fail closed while configured for ep-a")
	}
	if !strings.Contains(err.Error(), "does not match configured endpoint_name") {
		t.Fatalf("expected explicit endpoint_name mismatch error, got: %v", err)
	}
	if r.calls != 0 {
		t.Fatalf("unconfigured endpoint invoked runtime %d times; must fail before invoke", r.calls)
	}
}

// Provider streaming is not advertised anywhere for this contract.
func TestF2_StreamingNotAdvertised(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithStaticClients(&f2StubRuntime{}, &f2StubControl{}))
	desc, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe failed: %v", err)
	}
	if len(desc.Factories) != 1 {
		t.Fatalf("expected 1 factory, got %d", len(desc.Factories))
	}
	if desc.Factories[0].StaticCapabilities.Streaming {
		t.Fatalf("Describe must not advertise provider streaming")
	}

	r := &f2StubRuntime{}
	c := &f2StubControl{}
	inst := f2Configure(t, r, c, "ep-a")
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if profile.Capabilities.Streaming {
		t.Fatalf("Resolve must not advertise provider streaming")
	}
	listed, err := inst.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(listed.Models) != 1 || listed.Models[0].Capabilities.Streaming {
		t.Fatalf("ListModels must advertise exactly one non-streaming model, got %+v", listed.Models)
	}
}

// Streaming delivery requests are served via unary collect: the runtime
// InvokeEndpoint path succeeds and no event-stream API exists on the client.
func TestF2_StreamingDelivery_UsesUnaryCollect(t *testing.T) {
	t.Parallel()
	r := &f2StubRuntime{}
	c := &f2StubControl{}
	inst := f2Configure(t, r, c, "ep-a")

	stream := newTestExecuteStream(context.Background(), "sagemaker/ep-a", lipapi.OperationOpenAIChatCompletions, "hello", false, nil)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("streaming-delivery Execute failed: %v", err)
	}
	if r.calls != 1 {
		t.Fatalf("expected exactly 1 unary runtime call, got %d", r.calls)
	}
	var sawDelta, sawTerminal bool
	for _, f := range stream.outbox {
		if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil && f.Event.Kind == backendplugin.EventTextDelta && f.Event.Delta != nil && *f.Event.Delta == "ok" {
			sawDelta = true
		}
		if f.Kind == backendplugin.ServerFrameTerminal && f.Terminal != nil && f.Terminal.Status == backendplugin.TerminalSuccess {
			sawTerminal = true
		}
	}
	if !sawDelta {
		t.Fatalf("expected text delta \"ok\" via unary collect")
	}
	if !sawTerminal {
		t.Fatalf("expected terminal success")
	}
}

// Oversized unary responses fail closed on the explicit byte bound instead of
// growing an unbounded buffer.
func TestF2_UnaryOversizedResponse_BoundedError(t *testing.T) {
	t.Parallel()
	big := make([]byte, (4<<20)+1)
	for i := range big {
		big[i] = 'x'
	}
	r := &f2StubRuntime{
		invokeFn: func(context.Context, *sagemakerruntime.InvokeEndpointInput) (*sagemakerruntime.InvokeEndpointOutput, error) {
			return &sagemakerruntime.InvokeEndpointOutput{Body: big}, nil
		},
	}
	c := &f2StubControl{}
	inst := f2Configure(t, r, c, "ep-a")

	stream := newTestExecuteStream(context.Background(), "sagemaker/ep-a", lipapi.OperationOpenAIChatCompletions, "hello", true, nil)
	err := inst.Execute(stream)
	if err == nil {
		t.Fatalf("expected oversized response to fail closed")
	}
	if !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("expected bounded exceeds-limit error, got: %v", err)
	}
}
