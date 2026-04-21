package conformance

import (
	"context"
	"encoding/base64"
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
	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"google.golang.org/genai"
)

func TestConformance_Multimodal_imageInUpstream(t *testing.T) {
	t.Parallel()
	for _, cell := range AllCells() {
		if !cell.Meta.MultimodalViable {
			continue
		}
		t.Run(cell.Frontend+"__"+cell.Backend, func(t *testing.T) {
			t.Parallel()
			var captured string
			beSrv := NewSuccessRefBackend(t, cell.Backend, func(b []byte) { captured = string(b) })
			exec := NewTestExecutor(t, cell.Backend, beSrv.URL, beSrv.Client())
			route := RouteSelector(cell.Backend, DefaultModel(cell.Backend))
			mux := http.NewServeMux()
			MountFrontend(mux, cell.Frontend, exec, route)
			feSrv := httptest.NewServer(mux)
			t.Cleanup(feSrv.Close)

			png := refclienttest.ReadRefclientFixture(t, "tiny.png")
			multimodalImageOnly(t, cell.Frontend, feSrv.URL, feSrv.Client(), png)
			assertUpstreamImageMarker(t, cell.Backend, captured)
		})
	}
}

func TestConformance_Multimodal_pdfInUpstream(t *testing.T) {
	t.Parallel()
	for _, cell := range AllCells() {
		if !cell.Meta.MultimodalViable {
			continue
		}
		t.Run(cell.Frontend+"__"+cell.Backend, func(t *testing.T) {
			t.Parallel()
			var captured string
			beSrv := NewSuccessRefBackend(t, cell.Backend, func(b []byte) { captured = string(b) })
			exec := NewTestExecutor(t, cell.Backend, beSrv.URL, beSrv.Client())
			route := RouteSelector(cell.Backend, DefaultModel(cell.Backend))
			mux := http.NewServeMux()
			MountFrontend(mux, cell.Frontend, exec, route)
			feSrv := httptest.NewServer(mux)
			t.Cleanup(feSrv.Close)

			pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")
			multimodalPDFOnly(t, cell.Frontend, feSrv.URL, feSrv.Client(), pdf)
			assertUpstreamPDFMarker(t, cell.Backend, captured)
		})
	}
}

func multimodalImageOnly(tb testing.TB, frontendID, proxyOrigin string, httpClient *http.Client, png []byte) {
	tb.Helper()
	ctx := context.Background()
	imgB64 := base64.StdEncoding.EncodeToString(png)
	switch frontendID {
	case "openai-responses":
		cli := refopenairesponses.New(refopenairesponses.Config{
			BaseURL:    strings.TrimRight(proxyOrigin, "/") + "/v1",
			APIKey:     "sk-test",
			HTTPClient: httpClient,
		})
		dataImageURL := "data:image/png;base64," + imgB64
		img := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
		img.OfInputImage.ImageURL = openai.String(dataImageURL)
		_, err := cli.CreateResponse(ctx, responses.ResponseNewParams{
			Model: shared.ResponsesModel(wireModelForFrontend(frontendID)),
			Input: responses.ResponseNewParamsInputUnion{
				OfInputItemList: []responses.ResponseInputItemUnionParam{
					responses.ResponseInputItemParamOfInputMessage(
						responses.ResponseInputMessageContentListParam{
							responses.ResponseInputContentParamOfInputText("describe image"),
							img,
						},
						"user",
					),
				},
			},
		})
		if err != nil {
			tb.Fatalf("responses: %v", err)
		}
	case openailegacy.ID:
		cli := refopenaichat.New(refopenaichat.Config{
			BaseURL:    strings.TrimRight(proxyOrigin, "/") + "/v1",
			APIKey:     "sk-test",
			HTTPClient: httpClient,
		})
		imgURL := "data:image/png;base64," + imgB64
		parts := []openai.ChatCompletionContentPartUnionParam{
			openai.TextContentPart("describe"),
			openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{URL: imgURL}),
		}
		_, err := cli.CreateChatCompletion(ctx, openai.ChatCompletionNewParams{
			Model: shared.ChatModelGPT4oMini,
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.UserMessage(parts),
			},
		})
		if err != nil {
			tb.Fatalf("chat: %v", err)
		}
	case "anthropic":
		cli := refanthropic.New(refanthropic.Config{
			BaseURL:    proxyOrigin,
			APIKey:     "sk-ant-test",
			HTTPClient: httpClient,
		})
		img := anthropic.NewImageBlock(anthropic.Base64ImageSourceParam{
			Data:      imgB64,
			MediaType: anthropic.Base64ImageSourceMediaTypeImagePNG,
		})
		_, err := cli.CreateMessage(ctx, anthropic.MessageNewParams{
			Model:     anthropic.Model(wireModelForFrontend(frontendID)),
			MaxTokens: 128,
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock("describe"), img),
			},
		})
		if err != nil {
			tb.Fatalf("anthropic: %v", err)
		}
	case "gemini":
		cli, err := refgemini.New(ctx, refgemini.Config{
			BaseURL:    proxyOrigin,
			APIKey:     "fake-key",
			HTTPClient: httpClient,
		})
		if err != nil {
			tb.Fatalf("gemini client: %v", err)
		}
		contents := []*genai.Content{{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				{Text: "describe"},
				{InlineData: &genai.Blob{MIMEType: "image/png", Data: png}},
			},
		}}
		_, err = cli.GenerateContent(ctx, wireModelForFrontend(frontendID), contents, nil)
		if err != nil {
			tb.Fatalf("gemini: %v", err)
		}
	default:
		tb.Fatalf("unknown frontend %q", frontendID)
	}
}

func multimodalPDFOnly(tb testing.TB, frontendID, proxyOrigin string, httpClient *http.Client, pdf []byte) {
	tb.Helper()
	ctx := context.Background()
	pdfB64 := base64.StdEncoding.EncodeToString(pdf)
	switch frontendID {
	case "openai-responses":
		cli := refopenairesponses.New(refopenairesponses.Config{
			BaseURL:    strings.TrimRight(proxyOrigin, "/") + "/v1",
			APIKey:     "sk-test",
			HTTPClient: httpClient,
		})
		filePart := responses.ResponseInputContentUnionParam{
			OfInputFile: &responses.ResponseInputFileParam{
				FileData: openai.String(pdfB64),
				Filename: openai.String("minimal.pdf"),
			},
		}
		_, err := cli.CreateResponse(ctx, responses.ResponseNewParams{
			Model: shared.ResponsesModel(wireModelForFrontend(frontendID)),
			Input: responses.ResponseNewParamsInputUnion{
				OfInputItemList: []responses.ResponseInputItemUnionParam{
					responses.ResponseInputItemParamOfInputMessage(
						responses.ResponseInputMessageContentListParam{
							responses.ResponseInputContentParamOfInputText("summarize pdf"),
							filePart,
						},
						"user",
					),
				},
			},
		})
		if err != nil {
			tb.Fatalf("responses: %v", err)
		}
	case openailegacy.ID:
		cli := refopenaichat.New(refopenaichat.Config{
			BaseURL:    strings.TrimRight(proxyOrigin, "/") + "/v1",
			APIKey:     "sk-test",
			HTTPClient: httpClient,
		})
		parts := []openai.ChatCompletionContentPartUnionParam{
			openai.TextContentPart("summarize"),
			openai.FileContentPart(openai.ChatCompletionContentPartFileFileParam{
				FileData: openai.String(pdfB64),
				Filename: openai.String("minimal.pdf"),
			}),
		}
		_, err := cli.CreateChatCompletion(ctx, openai.ChatCompletionNewParams{
			Model: shared.ChatModelGPT4oMini,
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.UserMessage(parts),
			},
		})
		if err != nil {
			tb.Fatalf("chat: %v", err)
		}
	case "anthropic":
		cli := refanthropic.New(refanthropic.Config{
			BaseURL:    proxyOrigin,
			APIKey:     "sk-ant-test",
			HTTPClient: httpClient,
		})
		doc := anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{Data: pdfB64})
		_, err := cli.CreateMessage(ctx, anthropic.MessageNewParams{
			Model:     anthropic.Model(wireModelForFrontend(frontendID)),
			MaxTokens: 128,
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock("summarize"), doc),
			},
		})
		if err != nil {
			tb.Fatalf("anthropic: %v", err)
		}
	case "gemini":
		cli, err := refgemini.New(ctx, refgemini.Config{
			BaseURL:    proxyOrigin,
			APIKey:     "fake-key",
			HTTPClient: httpClient,
		})
		if err != nil {
			tb.Fatalf("gemini client: %v", err)
		}
		contents := []*genai.Content{{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				{Text: "summarize"},
				{InlineData: &genai.Blob{MIMEType: "application/pdf", Data: pdf}},
			},
		}}
		_, err = cli.GenerateContent(ctx, wireModelForFrontend(frontendID), contents, nil)
		if err != nil {
			tb.Fatalf("gemini: %v", err)
		}
	default:
		tb.Fatalf("unknown frontend %q", frontendID)
	}
}

func assertUpstreamImageMarker(tb testing.TB, backendID, captured string) {
	tb.Helper()
	lower := strings.ToLower(captured)
	switch backendID {
	case openairesponses.ID:
		if !strings.Contains(lower, "input_image") {
			tb.Fatalf("expected input_image in upstream body, got: %s", trim(captured, 500))
		}
	case openailegacy.ID:
		if !strings.Contains(lower, "image_url") {
			tb.Fatalf("expected image_url in upstream body, got: %s", trim(captured, 500))
		}
	case "anthropic":
		if !strings.Contains(lower, `"type":"image"`) {
			tb.Fatalf("expected image content block in upstream body, got: %s", trim(captured, 500))
		}
	case gemini.ID:
		if !strings.Contains(lower, "inlinedata") && !strings.Contains(lower, "inline_data") {
			tb.Fatalf("expected inline image payload in upstream body, got: %s", trim(captured, 500))
		}
	case bedrock.ID:
		if !strings.Contains(lower, "image") && !strings.Contains(lower, "png") {
			tb.Fatalf("expected image payload markers in upstream body, got: %s", trim(captured, 500))
		}
	default:
		tb.Fatalf("unexpected backend %q for multimodal image assertion", backendID)
	}
}

func assertUpstreamPDFMarker(tb testing.TB, backendID, captured string) {
	tb.Helper()
	lower := strings.ToLower(captured)
	switch backendID {
	case openairesponses.ID:
		if !strings.Contains(lower, "input_file") {
			tb.Fatalf("expected input_file in upstream body, got: %s", trim(captured, 500))
		}
	case openailegacy.ID:
		if !strings.Contains(lower, `"type":"file"`) && !strings.Contains(lower, "file_data") {
			tb.Fatalf("expected file part in upstream body, got: %s", trim(captured, 500))
		}
	case "anthropic":
		if !strings.Contains(lower, `"type":"document"`) {
			tb.Fatalf("expected document block in upstream body, got: %s", trim(captured, 500))
		}
	case gemini.ID:
		if !strings.Contains(lower, "application/pdf") && !strings.Contains(lower, "pdf") {
			tb.Fatalf("expected pdf payload markers in upstream body, got: %s", trim(captured, 500))
		}
	case bedrock.ID:
		if !strings.Contains(lower, "pdf") && !strings.Contains(lower, "document") {
			tb.Fatalf("expected pdf/document markers in upstream body, got: %s", trim(captured, 500))
		}
	default:
		tb.Fatalf("unexpected backend %q for multimodal pdf assertion", backendID)
	}
}
