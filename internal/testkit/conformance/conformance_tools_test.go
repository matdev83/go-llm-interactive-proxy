package conformance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	refanthropic "github.com/matdev83/go-llm-interactive-proxy/internal/refclient/anthropicmessages"
	refgemini "github.com/matdev83/go-llm-interactive-proxy/internal/refclient/gemini"
	refopenaichat "github.com/matdev83/go-llm-interactive-proxy/internal/refclient/openaichat"
	refopenairesponses "github.com/matdev83/go-llm-interactive-proxy/internal/refclient/openairesponses"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"google.golang.org/genai"
)

func TestConformance_Tools_roundTripAndUsage(t *testing.T) {
	t.Parallel()
	for _, cell := range AllCells() {
		if !cell.Meta.ToolsViable {
			continue
		}
		t.Run(cell.Frontend+"__"+cell.Backend, func(t *testing.T) {
			t.Parallel()
			var captured string
			beSrv := NewToolRefBackend(t, cell.Backend, func(b []byte) { captured = string(b) })
			exec := NewTestExecutor(t, cell.Backend, beSrv.URL, beSrv.Client())
			route := RouteSelector(cell.Backend, DefaultModel(cell.Backend))
			mux := http.NewServeMux()
			if err := MountFrontend(mux, cell.Frontend, exec, route); err != nil {
				t.Fatal(err)
			}
			feSrv := httptest.NewServer(mux)
			t.Cleanup(feSrv.Close)

			raw := toolStreamRawJoined(t, cell.Frontend, feSrv.URL, feSrv.Client(), cell.Backend)
			name := toolNameForBackend(cell.Backend)
			if !strings.Contains(strings.ToLower(captured), strings.ToLower(name)) {
				t.Fatalf("upstream request should include tool name %q, body prefix: %s", name, trim(captured, 800))
			}
			if !strings.Contains(strings.ToLower(raw), strings.ToLower(name)) {
				t.Fatalf("client-visible stream should include tool name %q, joined: %s", name, trim(raw, 1200))
			}
			lower := strings.ToLower(raw)
			capLower := strings.ToLower(captured)
			if !stringsContainsAny(lower, []string{"input_tokens", "prompt_tokens", "prompttokencount", "total_tokens", "totaltokencount", "usagemetadata"}) &&
				!stringsContainsAny(capLower, []string{"input_tokens", "usage", "prompt_tokens", "total_tokens", "usagemetadata"}) {
				t.Fatalf("expected usage markers in client stream or captured upstream body, raw=%s cap=%s", trim(raw, 600), trim(captured, 600))
			}
		})
	}
}

func stringsContainsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func trim(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func toolStreamRawJoined(tb testing.TB, frontendID, proxyOrigin string, httpClient *http.Client, backendID string) string {
	tb.Helper()
	ctx := context.Background()
	name := toolNameForBackend(backendID)
	switch frontendID {
	case "openai-responses":
		cli := refopenairesponses.New(refopenairesponses.Config{
			BaseURL:    strings.TrimRight(proxyOrigin, "/") + "/v1",
			APIKey:     "sk-test",
			HTTPClient: httpClient,
		})
		params := responses.ResponseNewParams{
			Model: shared.ResponsesModel(wireModelForFrontend(frontendID)),
			Input: responses.ResponseNewParamsInputUnion{
				OfInputItemList: []responses.ResponseInputItemUnionParam{
					responses.ResponseInputItemParamOfMessage("weather?", responses.EasyInputMessageRoleUser),
				},
			},
			Tools: []responses.ToolUnionParam{{
				OfFunction: &responses.FunctionToolParam{
					Name:        name,
					Description: openai.String("tool"),
					Parameters:  toolParamsMap(backendID),
				},
			}},
		}
		stream := cli.CreateResponseStream(ctx, params)
		var b strings.Builder
		for stream.Next() {
			raw, _ := json.Marshal(stream.Current())
			b.WriteString(string(raw))
		}
		if err := stream.Err(); err != nil {
			tb.Fatalf("responses stream: %v", err)
		}
		return b.String()
	case "openai-legacy":
		cli := refopenaichat.New(refopenaichat.Config{
			BaseURL:    strings.TrimRight(proxyOrigin, "/") + "/v1",
			APIKey:     "sk-test",
			HTTPClient: httpClient,
		})
		params := openai.ChatCompletionNewParams{
			Model: shared.ChatModelGPT4oMini,
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.UserMessage("weather?"),
			},
			Tools: []openai.ChatCompletionToolUnionParam{
				openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
					Name:        name,
					Description: openai.String("tool"),
					Parameters:  shared.FunctionParameters(toolParamsMap(backendID)),
				}),
			},
			ToolChoice: openai.ToolChoiceOptionFunctionToolChoice(openai.ChatCompletionNamedToolChoiceFunctionParam{
				Name: name,
			}),
			StreamOptions: openai.ChatCompletionStreamOptionsParam{
				IncludeUsage: openai.Bool(true),
			},
		}
		stream := cli.CreateChatCompletionStream(ctx, params)
		var b strings.Builder
		for stream.Next() {
			raw, _ := json.Marshal(stream.Current())
			b.WriteString(string(raw))
		}
		if err := stream.Err(); err != nil {
			tb.Fatalf("chat stream: %v", err)
		}
		return b.String()
	case "anthropic":
		cli := refanthropic.New(refanthropic.Config{
			BaseURL:    proxyOrigin,
			APIKey:     "sk-ant-test",
			HTTPClient: httpClient,
		})
		params := anthropic.MessageNewParams{
			Model:     anthropic.Model(wireModelForFrontend(frontendID)),
			MaxTokens: 256,
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock("weather?")),
			},
			Tools: []anthropic.ToolUnionParam{{
				OfTool: &anthropic.ToolParam{
					Name:        name,
					Description: anthropic.String("tool"),
					InputSchema: mustAnthropicToolSchema(tb, backendID),
				},
			}},
			ToolChoice: anthropic.ToolChoiceUnionParam{
				OfTool: &anthropic.ToolChoiceToolParam{Name: name},
			},
		}
		stream := cli.CreateMessageStream(ctx, params)
		var b strings.Builder
		for stream.Next() {
			raw, _ := json.Marshal(stream.Current())
			b.WriteString(string(raw))
		}
		if err := stream.Err(); err != nil {
			tb.Fatalf("anthropic stream: %v", err)
		}
		return b.String()
	case "gemini":
		cli, err := refgemini.New(ctx, refgemini.Config{
			BaseURL:    proxyOrigin,
			APIKey:     "fake-key",
			HTTPClient: httpClient,
		})
		if err != nil {
			tb.Fatalf("gemini client: %v", err)
		}
		tools := []*genai.Tool{{
			FunctionDeclarations: []*genai.FunctionDeclaration{{
				Name: name,
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"city": {Type: genai.TypeString},
					},
				},
			}},
		}}
		cfg := &genai.GenerateContentConfig{Tools: tools}
		var b strings.Builder
		for res, serr := range cli.GenerateContentStream(ctx, wireModelForFrontend(frontendID),
			[]*genai.Content{genai.NewContentFromText("weather?", genai.RoleUser)}, cfg) {
			if serr != nil {
				tb.Fatalf("gemini stream: %v", serr)
			}
			raw, _ := json.Marshal(res)
			b.WriteString(string(raw))
		}
		return b.String()
	default:
		tb.Fatalf("unknown frontend %q", frontendID)
		return ""
	}
}

func mustAnthropicToolSchema(tb testing.TB, backendID string) anthropic.ToolInputSchemaParam {
	tb.Helper()
	raw, err := json.Marshal(toolParamsMap(backendID))
	if err != nil {
		tb.Fatal(err)
	}
	var s anthropic.ToolInputSchemaParam
	if err := json.Unmarshal(raw, &s); err != nil {
		tb.Fatal(err)
	}
	return s
}

func toolParamsMap(backendID string) map[string]any {
	if backendID == gemini.ID {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
			},
		}
	}
	if backendID == openairesponses.ID {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"q": map[string]any{"type": "integer"},
			},
		}
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city": map[string]any{"type": "string"},
		},
	}
}
