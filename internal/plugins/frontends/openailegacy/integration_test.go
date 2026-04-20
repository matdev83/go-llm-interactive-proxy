package openailegacy_test

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	front "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	refcli "github.com/matdev83/go-llm-interactive-proxy/internal/refclient/openaichat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

func TestIntegration_refclientNonStreaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "integration-ok", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	res, err := cli.CreateChatCompletion(context.Background(), openai.ChatCompletionNewParams{
		Model: shared.ChatModelGPT4oMini,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("ping"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Choices) == 0 || res.Choices[0].Message.Content != "integration-ok" {
		t.Fatalf("choices: %+v", res.Choices)
	}
}

func TestIntegration_refclientStreaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "stream-ok", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	stream := cli.CreateChatCompletionStream(context.Background(), openai.ChatCompletionNewParams{
		Model: shared.ChatModelGPT4oMini,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("hi"),
		},
	})
	var got string
	for stream.Next() {
		ch := stream.Current()
		for _, c := range ch.Choices {
			if c.Delta.Content != "" {
				got += c.Delta.Content
			}
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if got != "stream-ok" {
		t.Fatalf("delta content: got %q", got)
	}
}

func TestIntegration_refclientMultimodalCanonicalParts(t *testing.T) {
	t.Parallel()
	var capture sync.Map
	caps := lipapi.NewBackendCaps(
		lipapi.CapabilityStreaming,
		lipapi.CapabilityVision,
		lipapi.CapabilityDocuments,
	)
	ex := testkit.NewStubExecutor(t, caps, "seen", &capture)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")
	imgURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	pdfB64 := base64.StdEncoding.EncodeToString(pdf)

	parts := []openai.ChatCompletionContentPartUnionParam{
		openai.TextContentPart("summarize"),
		openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{URL: imgURL}),
		openai.FileContentPart(openai.ChatCompletionContentPartFileFileParam{
			FileData: openai.String(pdfB64),
			Filename: openai.String("minimal.pdf"),
		}),
	}

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	_, err := cli.CreateChatCompletion(context.Background(), openai.ChatCompletionNewParams{
		Model: shared.ChatModelGPT4oMini,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(parts),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := capture.Load("last")
	if !ok {
		t.Fatal("expected captured call")
	}
	call := v.(lipapi.Call)
	partsOut := call.Messages[0].Parts
	if len(partsOut) < 3 {
		t.Fatalf("parts: %+v", partsOut)
	}
	if partsOut[1].Kind != lipapi.PartImageRef {
		t.Fatalf("want image part, got %+v", partsOut[1])
	}
	if partsOut[2].Kind != lipapi.PartFileRef || partsOut[2].FileMIME != "application/pdf" {
		t.Fatalf("want pdf file part, got %+v", partsOut[2])
	}
}

func TestIntegration_malformedJSON_returns400(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "x", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	res, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}
}

func TestIntegration_capabilityReject_returns400(t *testing.T) {
	t.Parallel()
	// Stub has vision+documents but negotiation still needs streaming; omit vision to force reject on multimodal user parts.
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "nope", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	imgURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	_, err := cli.CreateChatCompletion(context.Background(), openai.ChatCompletionNewParams{
		Model: shared.ChatModelGPT4oMini,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage([]openai.ChatCompletionContentPartUnionParam{
				openai.TextContentPart("x"),
				openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{URL: imgURL}),
			}),
		},
	})
	if err == nil {
		t.Fatal("expected error from capability reject")
	}
}
