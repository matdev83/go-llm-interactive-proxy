//go:build integration

package conformance

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
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

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	corerepair "github.com/matdev83/go-llm-interactive-proxy/internal/core/toolcallrepair"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	featuretoolrepair "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolcallrepair"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
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

func TestConformance_ToolCallRepairCanonicalMatrix(t *testing.T) {
	t.Parallel()
	for _, cell := range AllCells() {
		if !cell.Meta.ToolsViable {
			continue
		}
		t.Run(cell.Frontend+"__"+cell.Backend, func(t *testing.T) {
			t.Parallel()
			if cell.Frontend == "gemini" || cell.Backend == gemini.ID {
				t.Skip("gemini wire materializes functionCall.args as a JSON object; syntax truncation is not exercisable")
			}
			beSrv := NewToolCallRepairRefBackend(t, cell.Backend)
			exec := NewTestExecutor(t, cell.Backend, beSrv.URL, beSrv.Client())
			fin := corerepair.NewFinalizer(corerepair.FinalizerPolicy{
				ID:             featuretoolrepair.ID,
				MaxArgsBytes:   featuretoolrepair.DefaultMaxArgsBytes,
				OnUnrepairable: corerepair.OnUnrepairablePassThrough,
				Order:          corerepair.DefaultFinalizerOrder,
				Schema:         corerepair.DefaultSchemaLimits(),
			})
			exec.SetToolCallFinalizers([]toolcall.Finalizer{fin}, featuretoolrepair.DefaultMaxArgsBytes)
			exec.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(exec.Bus, extensions.SnapshotOptions{
				ToolCallFinalizers: []toolcall.Finalizer{fin},
			})
			route := RouteSelector(cell.Backend, DefaultModel(cell.Backend))
			mux := http.NewServeMux()
			if err := MountFrontend(mux, cell.Frontend, exec, route); err != nil {
				t.Fatal(err)
			}
			feSrv := httptest.NewServer(mux)
			t.Cleanup(feSrv.Close)

			raw, gotArgs := toolStreamRawAndArgs(t, cell.Frontend, feSrv.URL, feSrv.Client(), cell.Backend)
			name := toolNameForBackend(cell.Backend)
			if !strings.Contains(strings.ToLower(raw), strings.ToLower(name)) {
				t.Fatalf("repaired canonical lifecycle lost tool name %q: %s", name, trim(raw, 1200))
			}
			want := WantRepairedToolArgsJSON(cell.Backend)
			// Assert on decoded wire args (not json.Marshal of SDK events, which escapes quotes).
			if gotArgs != want {
				t.Fatalf("client-visible tool args %q want closed repaired %q; stream=%s", gotArgs, want, trim(raw, 1600))
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
	raw, _ := toolStreamRawAndArgs(tb, frontendID, proxyOrigin, httpClient, backendID)
	return raw
}

// toolStreamRawAndArgs joins marshaled stream events and returns decoded tool-arguments JSON.
func toolStreamRawAndArgs(tb testing.TB, frontendID, proxyOrigin string, httpClient *http.Client, backendID string) (raw string, argsJSON string) {
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
			ev := stream.Current()
			enc, _ := json.Marshal(ev)
			b.Write(enc)
			if ev.Type == "response.function_call_arguments.done" {
				argsJSON = ev.AsResponseFunctionCallArgumentsDone().Arguments
			}
		}
		if err := stream.Err(); err != nil {
			tb.Fatalf("responses stream: %v", err)
		}
		return b.String(), argsJSON
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
		var argsBuilder strings.Builder
		for stream.Next() {
			ev := stream.Current()
			enc, _ := json.Marshal(ev)
			b.Write(enc)
			if len(ev.Choices) == 0 {
				continue
			}
			for _, tc := range ev.Choices[0].Delta.ToolCalls {
				if tc.Function.Arguments != "" {
					argsBuilder.WriteString(tc.Function.Arguments)
				}
			}
		}
		if err := stream.Err(); err != nil {
			tb.Fatalf("chat stream: %v", err)
		}
		return b.String(), argsBuilder.String()
	case "anthropic":
		cli := refanthropic.New(refanthropic.Config{
			BaseURL:    proxyOrigin,
			APIKey:     testkit.SyntheticAnthropicAPIKey,
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
		var argsBuilder strings.Builder
		for stream.Next() {
			ev := stream.Current()
			enc, _ := json.Marshal(ev)
			b.Write(enc)
			if ev.Type == "content_block_delta" {
				if d := ev.AsContentBlockDelta().Delta; d.Type == "input_json_delta" {
					argsBuilder.WriteString(d.PartialJSON)
				}
			}
		}
		if err := stream.Err(); err != nil {
			tb.Fatalf("anthropic stream: %v", err)
		}
		return b.String(), argsBuilder.String()
	case "gemini":
		cli, err := refgemini.New(ctx, refgemini.Config{
			BaseURL:    GeminiConformanceBaseURL(proxyOrigin),
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
			enc, _ := json.Marshal(res)
			b.Write(enc)
			if argsJSON == "" {
				argsJSON = geminiFunctionCallArgsJSON(res)
			}
		}
		return b.String(), argsJSON
	case "openresponses":
		return openResponsesToolStreamRawAndArgs(tb, proxyOrigin, httpClient, backendID)
	default:
		tb.Fatalf("unknown frontend %q", frontendID)
		return "", ""
	}
}

// openResponsesToolStreamRawAndArgs drives one streaming tool create against the
// OpenResponses frontend and returns the joined SSE event payloads plus the
// decoded function-call arguments JSON.
func openResponsesToolStreamRawAndArgs(tb testing.TB, proxyOrigin string, httpClient *http.Client, backendID string) (raw, argsJSON string) {
	tb.Helper()
	name := toolNameForBackend(backendID)
	schema, err := json.Marshal(toolParamsMap(backendID))
	if err != nil {
		tb.Fatalf("openresponses tools schema: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"model":  wireModelForFrontend("openresponses"),
		"stream": true,
		"store":  false,
		"input": []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "weather?"}},
		}},
		"tools": []any{map[string]any{
			"type": "function", "name": name, "description": "tool",
			"parameters": json.RawMessage(schema),
		}},
	})
	if err != nil {
		tb.Fatalf("openresponses tools body: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		strings.TrimRight(proxyOrigin, "/")+"/openresponses/v1/responses", strings.NewReader(string(body)))
	if err != nil {
		tb.Fatalf("openresponses tools request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		tb.Fatalf("openresponses tools post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		rb, _ := io.ReadAll(resp.Body)
		tb.Fatalf("openresponses tools status %d body=%s", resp.StatusCode, string(rb))
	}
	br := bufio.NewReader(resp.Body)
	var b strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			tb.Fatalf("openresponses tools read: %v", err)
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if payload == "[DONE]" {
				break
			}
			b.WriteString(payload)
			var ev struct {
				Type      string `json:"type"`
				Arguments string `json:"arguments"`
			}
			if json.Unmarshal([]byte(payload), &ev) == nil && ev.Type == "response.function_call_arguments.done" {
				argsJSON = ev.Arguments
			}
		}
		if err == io.EOF {
			break
		}
	}
	return b.String(), argsJSON
}

func geminiFunctionCallArgsJSON(res *genai.GenerateContentResponse) string {
	if res == nil {
		return ""
	}
	for _, c := range res.Candidates {
		if c == nil || c.Content == nil {
			continue
		}
		for _, p := range c.Content.Parts {
			if p == nil || p.FunctionCall == nil || p.FunctionCall.Args == nil {
				continue
			}
			enc, err := json.Marshal(p.FunctionCall.Args)
			if err != nil {
				return ""
			}
			return string(enc)
		}
	}
	return ""
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
