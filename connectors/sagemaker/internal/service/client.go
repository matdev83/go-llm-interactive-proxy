package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sagemaker"
	sagemakertypes "github.com/aws/aws-sdk-go-v2/service/sagemaker/types"
	"github.com/aws/aws-sdk-go-v2/service/sagemakerruntime"
	runtimetypes "github.com/aws/aws-sdk-go-v2/service/sagemakerruntime/types"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

type HFTextGenRequest struct {
	Inputs     string           `json:"inputs"`
	Parameters *HFTextGenParams `json:"parameters,omitempty"`
}

type HFTextGenParams struct {
	MaxNewTokens *int `json:"max_new_tokens,omitempty"`
}

type Client struct {
	Config  Config
	Runtime RuntimeClient
	Control ControlClient
}

func parseHFTextGenResponse(raw []byte) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", fmt.Errorf("sagemaker: empty response body")
	}
	if raw[0] == '[' {
		var arr []struct {
			GeneratedText string `json:"generated_text"`
		}
		if err := json.Unmarshal(raw, &arr); err != nil {
			return "", fmt.Errorf("sagemaker: unmarshal array response: %w", err)
		}
		if len(arr) == 0 {
			return "", fmt.Errorf("sagemaker: empty response array")
		}
		return arr[0].GeneratedText, nil
	}

	var obj struct {
		GeneratedText string `json:"generated_text"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", fmt.Errorf("sagemaker: unmarshal object response: %w", err)
	}
	return obj.GeneratedText, nil
}

func (c *Client) Open(ctx context.Context, call lipapi.Call, model string) (lipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, lipapi.ErrNilContext
	}
	if len(call.Tools) > 0 {
		return nil, fmt.Errorf("sagemaker: tools are unsupported in this connector")
	}

	var inputsBuilder strings.Builder
	for _, m := range call.Messages {
		if m.Role != lipapi.RoleUser {
			return nil, fmt.Errorf("sagemaker: role %q is unsupported in hf-text-generation contract", m.Role)
		}
		for _, p := range m.Parts {
			if p.Kind != lipapi.PartText {
				return nil, fmt.Errorf("sagemaker: part kind %q is unsupported in hf-text-generation contract", p.Kind)
			}
			inputsBuilder.WriteString(p.Text)
		}
	}
	inputs := inputsBuilder.String()
	if strings.TrimSpace(inputs) == "" {
		return nil, fmt.Errorf("sagemaker: empty inputs")
	}

	var params *HFTextGenParams
	if call.Options.MaxOutputTokens != nil {
		params = &HFTextGenParams{
			MaxNewTokens: call.Options.MaxOutputTokens,
		}
	}

	reqBody := HFTextGenRequest{
		Inputs:     inputs,
		Parameters: params,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("sagemaker: marshal request: %w", err)
	}

	stream := call.Invocation.DeliveryMode != lipapi.DeliveryModeNonStreaming &&
		call.Invocation.TransportMode != lipapi.TransportModeNonStreaming

	if stream {
		streamInput := &sagemakerruntime.InvokeEndpointWithResponseStreamInput{
			EndpointName: aws.String(model),
			ContentType:  aws.String("application/json"),
			Accept:       aws.String("application/json"),
			Body:         bodyBytes,
		}
		if c.Config.InferenceComponentName != "" {
			streamInput.InferenceComponentName = aws.String(c.Config.InferenceComponentName)
		}

		resp, err := c.Runtime.InvokeEndpointWithResponseStream(ctx, streamInput)
		if err != nil {
			return nil, fmt.Errorf("sagemaker: invoke endpoint with stream: %w", err)
		}
		return lipapi.CloseOnlyManagedStream{Stream: newSageMakerResponseStream(ctx, resp.GetStream())}, nil
	}

	input := &sagemakerruntime.InvokeEndpointInput{
		EndpointName: aws.String(model),
		ContentType:  aws.String("application/json"),
		Accept:       aws.String("application/json"),
		Body:         bodyBytes,
	}
	if c.Config.TargetModel != "" {
		input.TargetModel = aws.String(c.Config.TargetModel)
	}
	if c.Config.InferenceComponentName != "" {
		input.InferenceComponentName = aws.String(c.Config.InferenceComponentName)
	}

	resp, err := c.Runtime.InvokeEndpoint(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("sagemaker: invoke endpoint: %w", err)
	}

	genText, err := parseHFTextGenResponse(resp.Body)
	if err != nil {
		return nil, err
	}

	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: genText},
		{Kind: lipapi.EventResponseFinished},
	}
	return lipapi.CloseOnlyManagedStream{Stream: &sliceStream{events: events}}, nil
}

func (c *Client) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	input := &sagemaker.ListEndpointsInput{
		StatusEquals: sagemakertypes.EndpointStatusInService,
	}
	if limit > 0 && limit <= 100 {
		maxResults := int32(limit)
		input.MaxResults = &maxResults
	}

	resp, err := c.Control.ListEndpoints(ctx, input)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("sagemaker: list endpoints: %w", err)
	}

	out := make([]backendplugin.ModelDescriptor, 0, len(resp.Endpoints))
	for _, ep := range resp.Endpoints {
		if ep.EndpointName == nil {
			continue
		}
		if ep.EndpointStatus != "" && ep.EndpointStatus != sagemakertypes.EndpointStatusInService {
			continue
		}
		name := *ep.EndpointName
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: FactoryKind + "/" + name,
			NativeModelID:    name,
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		})
		if limit > 0 && uint32(len(out)) >= limit {
			break
		}
	}

	return backendplugin.ListModelsResponse{
		Models:          out,
		InventorySource: FactoryKind,
		FetchedUnixMS:   time.Now().UnixMilli(),
	}, nil
}

type sliceStream struct {
	mu     sync.Mutex
	events []lipapi.Event
	idx    int
	closed bool
}

func (s *sliceStream) Recv(context.Context) (lipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return lipapi.Event{}, fmt.Errorf("sagemaker: stream closed")
	}
	if s.idx >= len(s.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *sliceStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

type sagemakerEventStream interface {
	Events() <-chan runtimetypes.ResponseStream
	Close() error
	Err() error
}

type sagemakerStream struct {
	ctx      context.Context
	stream   sagemakerEventStream
	mu       sync.Mutex
	events   []lipapi.Event
	idx      int
	consumed bool
	closed   bool
}

func newSageMakerResponseStream(ctx context.Context, stream sagemakerEventStream) *sagemakerStream {
	return &sagemakerStream{
		ctx:    ctx,
		stream: stream,
	}
}

func (s *sagemakerStream) Recv(ctx context.Context) (lipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return lipapi.Event{}, fmt.Errorf("sagemaker: stream closed")
	}

	if !s.consumed {
		s.consumed = true
		var buf bytes.Buffer
		if s.stream != nil {
			for event := range s.stream.Events() {
				if err := ctx.Err(); err != nil {
					return lipapi.Event{}, err
				}
				if part, ok := event.(*runtimetypes.ResponseStreamMemberPayloadPart); ok {
					buf.Write(part.Value.Bytes)
				}
			}
			if err := s.stream.Err(); err != nil && err != io.EOF {
				return lipapi.Event{}, fmt.Errorf("sagemaker: stream error: %w", err)
			}
		}

		genText, err := parseHFTextGenResponse(buf.Bytes())
		if err != nil {
			return lipapi.Event{}, err
		}

		s.events = []lipapi.Event{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventMessageStarted},
			{Kind: lipapi.EventTextDelta, Delta: genText},
			{Kind: lipapi.EventResponseFinished},
		}
	}

	if s.idx >= len(s.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *sagemakerStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.stream != nil {
		return s.stream.Close()
	}
	return nil
}
