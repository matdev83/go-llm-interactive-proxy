//go:build integration

package conformance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"

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
			var captured []string
			beSrv := NewSuccessRefBackend(t, cell.Backend, func(b []byte) { captured = append(captured, string(b)) })
			exec := NewTestExecutor(t, cell.Backend, beSrv.URL, beSrv.Client())
			route := RouteSelector(cell.Backend, DefaultModel(cell.Backend))
			mux := http.NewServeMux()
			if err := MountFrontend(mux, cell.Frontend, exec, route); err != nil {
				t.Fatal(err)
			}
			feSrv := httptest.NewServer(mux)
			t.Cleanup(feSrv.Close)

			png := refclienttest.ReadRefclientFixture(t, "tiny.png")
			multimodalImageOnly(t, cell.Frontend, feSrv.URL, feSrv.Client(), png)
			// ACP (and other streaming connectors) may emit a trailing
			// best-effort cancel/close RPC after the terminal event, so the
			// projection marker is asserted against any captured upstream
			// request, matching the row/general "any request" evidence checks.
			assertUpstreamImageMarker(t, cell.Backend, strings.Join(captured, "\n"))
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
			if !multimodalPDFCellPositive(cell.Frontend, cell.Backend) {
				// The OpenResponses profile has no document/file input surface, so
				// cells whose frontend is OpenResponses reject before network; the
				// generic OpenResponses backend likewise rejects canonical file parts
				// as unrepresentable (Requirement 13.16 negative evidence).
				assertMultimodalPDFRejectedBeforeNetwork(t, cell.Frontend, cell.Backend)
				return
			}
			var captured []string
			beSrv := NewSuccessRefBackend(t, cell.Backend, func(b []byte) { captured = append(captured, string(b)) })
			exec := NewTestExecutor(t, cell.Backend, beSrv.URL, beSrv.Client())
			route := RouteSelector(cell.Backend, DefaultModel(cell.Backend))
			mux := http.NewServeMux()
			if err := MountFrontend(mux, cell.Frontend, exec, route); err != nil {
				t.Fatal(err)
			}
			feSrv := httptest.NewServer(mux)
			t.Cleanup(feSrv.Close)

			pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")
			multimodalPDFOnly(t, cell.Frontend, feSrv.URL, feSrv.Client(), pdf)
			assertUpstreamPDFMarker(t, cell.Backend, strings.Join(captured, "\n"))
		})
	}
}

// multimodalPDFCellPositive reports whether a cell can represent document/file
// input on the upstream wire. The OpenResponses frontend profile and the generic
// OpenResponses backend have no document surface, so those cells reject before
// network (Requirement 13.16).
func multimodalPDFCellPositive(frontend, backend string) bool {
	if frontend == "openresponses" {
		return false
	}
	if backend == "openresponses" {
		return false
	}
	return true
}

// assertMultimodalPDFRejectedBeforeNetwork drives a PDF create through the cell
// and asserts the rejection happens before any upstream request.
func assertMultimodalPDFRejectedBeforeNetwork(t *testing.T, frontend, backend string) {
	t.Helper()
	var captured atomic.Int64
	beSrv := NewSuccessRefBackend(t, backend, func([]byte) { captured.Add(1) })
	exec := NewTestExecutor(t, backend, beSrv.URL, beSrv.Client())
	route := RouteSelector(backend, DefaultModel(backend))
	mux := http.NewServeMux()
	if err := MountFrontend(mux, frontend, exec, route); err != nil {
		t.Fatal(err)
	}
	feSrv := httptest.NewServer(mux)
	t.Cleanup(feSrv.Close)

	pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")
	err := multimodalPDFExpectReject(t, frontend, feSrv.URL, feSrv.Client(), pdf)
	if err == nil {
		t.Fatalf("cell %s × %s: PDF create unexpectedly round-tripped", frontend, backend)
	}
	if n := captured.Load(); n != 0 {
		t.Fatalf("cell %s × %s: PDF rejection caused %d upstream requests, want 0", frontend, backend, n)
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
			APIKey:     testkit.SyntheticAnthropicAPIKey,
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
			BaseURL:    GeminiConformanceBaseURL(proxyOrigin),
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
	case "openresponses":
		imgB64 := base64.StdEncoding.EncodeToString(png)
		openResponsesMultimodalPost(tb, proxyOrigin, httpClient, map[string]any{
			"model": wireModelForFrontend("openresponses"),
			"store": false,
			"input": []any{map[string]any{
				"type": "message", "role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "describe image"},
					map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + imgB64},
				},
			}},
		})
	default:
		tb.Fatalf("unknown frontend %q", frontendID)
	}
}

// openResponsesMultimodalPost posts one multimodal create to the OpenResponses
// frontend and fails the test unless it round-trips successfully.
func openResponsesMultimodalPost(tb testing.TB, proxyOrigin string, httpClient *http.Client, body map[string]any) {
	tb.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		tb.Fatalf("openresponses multimodal body: %v", err)
	}
	resp, err := httpClient.Post(strings.TrimRight(proxyOrigin, "/")+"/openresponses/v1/responses", "application/json", strings.NewReader(string(raw)))
	if err != nil {
		tb.Fatalf("openresponses multimodal post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		rb, _ := io.ReadAll(resp.Body)
		tb.Fatalf("openresponses multimodal status %d body=%s", resp.StatusCode, string(rb))
	}
}

func multimodalPDFOnly(tb testing.TB, frontendID, proxyOrigin string, httpClient *http.Client, pdf []byte) {
	tb.Helper()
	if err := multimodalPDFRoundTrip(tb, frontendID, proxyOrigin, httpClient, pdf); err != nil {
		tb.Fatalf("%s: %v", frontendID, err)
	}
}

// multimodalPDFExpectReject drives a document create through the cell and
// returns the rejection error (used for pre-network negative evidence).
func multimodalPDFExpectReject(tb testing.TB, frontendID, proxyOrigin string, httpClient *http.Client, pdf []byte) error {
	tb.Helper()
	return multimodalPDFRoundTrip(tb, frontendID, proxyOrigin, httpClient, pdf)
}

func multimodalPDFRoundTrip(tb testing.TB, frontendID, proxyOrigin string, httpClient *http.Client, pdf []byte) error {
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
		return err
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
		return err
	case "anthropic":
		cli := refanthropic.New(refanthropic.Config{
			BaseURL:    proxyOrigin,
			APIKey:     testkit.SyntheticAnthropicAPIKey,
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
		return err
	case "gemini":
		cli, err := refgemini.New(ctx, refgemini.Config{
			BaseURL:    GeminiConformanceBaseURL(proxyOrigin),
			APIKey:     "fake-key",
			HTTPClient: httpClient,
		})
		if err != nil {
			return err
		}
		contents := []*genai.Content{{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				{Text: "summarize"},
				{InlineData: &genai.Blob{MIMEType: "application/pdf", Data: pdf}},
			},
		}}
		_, err = cli.GenerateContent(ctx, wireModelForFrontend(frontendID), contents, nil)
		return err
	case "openresponses":
		body, err := json.Marshal(map[string]any{
			"model": wireModelForFrontend("openresponses"),
			"store": false,
			"input": []any{map[string]any{
				"type": "message", "role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "summarize"},
					map[string]any{"type": "input_file", "file_data": pdfB64, "filename": "minimal.pdf"},
				},
			}},
		})
		if err != nil {
			return err
		}
		resp, err := httpClient.Post(strings.TrimRight(proxyOrigin, "/")+"/openresponses/v1/responses", "application/json", strings.NewReader(string(body)))
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("openresponses pdf status %d", resp.StatusCode)
		}
		return nil
	default:
		tb.Fatalf("unknown frontend %q", frontendID)
		return nil
	}
}

func assertUpstreamImageMarker(tb testing.TB, backendID, captured string) {
	tb.Helper()
	lower := strings.ToLower(captured)
	switch backendID {
	case openairesponses.ID:
		if !strings.Contains(lower, "input_image") && !strings.Contains(lower, "image_url") {
			tb.Fatalf("expected OpenAI-compatible image payload in upstream body, got: %s", trim(captured, 500))
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
	case BackendOpenResponses, BackendOpenRouter, BackendNVIDIA:
		if !strings.Contains(lower, "input_image") && !strings.Contains(lower, "image_url") {
			tb.Fatalf("expected OpenAI-compatible image payload in upstream body, got: %s", trim(captured, 500))
		}
	case BackendACP:
		if !strings.Contains(lower, `"resource"`) || !strings.Contains(lower, `"uri"`) {
			tb.Fatalf("expected ACP resource prompt block (image uri) in upstream body, got: %s", trim(captured, 500))
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
		if !strings.Contains(lower, "input_file") && !strings.Contains(lower, `"type":"file"`) &&
			!strings.Contains(lower, "file_data") {
			tb.Fatalf("expected OpenAI-compatible file payload in upstream body, got: %s", trim(captured, 500))
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
	case BackendOpenRouter, BackendNVIDIA:
		// The configured OpenAI-compatible provider-mode route forwards document
		// input as the OpenAI Responses file surface.
		if !strings.Contains(lower, "input_file") && !strings.Contains(lower, "file_data") {
			tb.Fatalf("expected OpenAI-compatible file payload in upstream body, got: %s", trim(captured, 500))
		}
	case BackendACP:
		if !strings.Contains(lower, `"resource"`) || !strings.Contains(lower, `"uri"`) ||
			!strings.Contains(lower, "pdf") {
			tb.Fatalf("expected ACP resource prompt block (pdf file uri/mime/name) in upstream body, got: %s", trim(captured, 500))
		}
	default:
		tb.Fatalf("unexpected backend %q for multimodal pdf assertion", backendID)
	}
}
