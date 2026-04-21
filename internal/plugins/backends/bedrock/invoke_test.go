package bedrock_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestConverseStreamInputForCall_textOnly(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "t1",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "anthropic.claude-3-haiku-20240307-v1:0"},
	}
	in, err := backend.ConverseStreamInputForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToString(in.ModelId) != "anthropic.claude-3-haiku-20240307-v1:0" {
		t.Fatalf("model: %v", in.ModelId)
	}
	if len(in.Messages) != 1 || in.Messages[0].Role != "user" {
		t.Fatalf("messages: %+v", in.Messages)
	}
}

func TestConverseStreamInputForCall_modelFromExtensions(t *testing.T) {
	t.Parallel()
	rawModel, _ := json.Marshal("us.anthropic.foo")
	call := lipapi.Call{
		ID: "t2",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("x")},
		}},
		Extensions: map[string]json.RawMessage{"bedrock.modelId": rawModel},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: backend.ID}}
	in, err := backend.ConverseStreamInputForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToString(in.ModelId) != "us.anthropic.foo" {
		t.Fatalf("model: %v", in.ModelId)
	}
}

func TestConverseStreamInputForCall_systemInstructions(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "sys",
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("Be brief.")},
		}},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	in, err := backend.ConverseStreamInputForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.System) != 1 {
		t.Fatalf("system len: %d", len(in.System))
	}
}

func TestConverseStreamInputForCall_multimodalWireJSON(t *testing.T) {
	t.Parallel()
	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")
	pngB64 := base64.StdEncoding.EncodeToString(png)
	pdfB64 := base64.StdEncoding.EncodeToString(pdf)

	call := lipapi.Call{
		ID: "mm",
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.TextPart("x"),
				{Kind: lipapi.PartImageRef, ImageRef: "data:image/png;base64," + pngB64},
				lipapi.FilePart("data:application/pdf;base64,"+pdfB64, "application/pdf", "f.pdf"),
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	in, err := backend.ConverseStreamInputForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, pngB64) || !strings.Contains(s, `"Format":"png"`) {
		t.Fatalf("expected png image payload in wire json: %s", s)
	}
	if !strings.Contains(s, pdfB64) || !strings.Contains(s, `"Format":"pdf"`) {
		t.Fatalf("expected pdf document payload in wire json: %s", s)
	}
}

func TestConverseStreamInputForCall_toolsAndToolChoice(t *testing.T) {
	t.Parallel()
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"q": map[string]any{"type": "string"},
		},
	})
	call := lipapi.Call{
		ID: "tools",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("weather?")},
		}},
		Tools: []lipapi.ToolDef{{
			Name:        "get_weather",
			Description: "wx",
			Parameters:  schema,
		}},
		ToolChoice: lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired, Name: "get_weather"},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	in, err := backend.ConverseStreamInputForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if in.ToolConfig == nil || len(in.ToolConfig.Tools) != 1 {
		t.Fatalf("tool config: %+v", in.ToolConfig)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "get_weather") {
		t.Fatalf("expected tool name in json: %s", s)
	}
}

func TestUpstreamError_returnsResponseError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"invalid request","__type":"ValidationException"}`))
	}))
	t.Cleanup(srv.Close)

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	cli := bedrockruntime.NewFromConfig(cfg, func(o *bedrockruntime.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
		o.EndpointOptions.DisableHTTPS = true
	})
	_, err = cli.ConverseStream(context.Background(), &bedrockruntime.ConverseStreamInput{
		ModelId: aws.String("m"),
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "x"}},
		}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var respErr *smithyhttp.ResponseError
	if !errors.As(err, &respErr) {
		t.Fatalf("expected *smithyhttp.ResponseError, got %T: %v", err, err)
	}
	if respErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("status: %d", respErr.HTTPStatusCode())
	}
}

func TestConverseStreamInputForCall_toolResultMessage(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "tool-res",
		Messages: []lipapi.Message{
			{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("call the tool")},
			},
			{
				Role: lipapi.RoleTool,
				Parts: []lipapi.Part{{
					Kind:       lipapi.PartToolResult,
					ToolCallID: "toolu_01",
					Content:    json.RawMessage(`{"ok":true}`),
				}},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	in, err := backend.ConverseStreamInputForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "ToolUseId") || !strings.Contains(s, "toolu_01") {
		t.Fatalf("expected toolResult mapping, got: %s", s)
	}
}
