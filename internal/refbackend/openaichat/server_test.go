package openaichat_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaichat"
	refcli "github.com/matdev83/go-llm-interactive-proxy/internal/refclient/openaichat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

func TestHandler_nonStreaming_refclientSmoke(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
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
	if res.ID != "chatcmpl_refbackend_1" {
		t.Fatalf("response id: got %q", res.ID)
	}
	if len(res.Choices) == 0 || res.Choices[0].Message.Content != "ok" {
		t.Fatalf("choices: %+v", res.Choices)
	}
}

func TestHandler_streaming_refclientReadsDelta(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
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

func TestHandler_requiresBearer(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: ""})
	_, err := cli.CreateChatCompletion(context.Background(), openai.ChatCompletionNewParams{
		Model: shared.ChatModelGPT4oMini,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("x"),
		},
	})
	if err == nil {
		t.Fatal("expected error without API key / bearer")
	}
}

func TestHandler_multimodalRequest_customJSON(t *testing.T) {
	t.Parallel()
	const mmJSON = `{
  "id": "chatcmpl_mm_out",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "gpt-4o-mini",
  "choices": [{"index":0,"message":{"role":"assistant","content":"multimodal-out:image+pdf"},"finish_reason":"stop"}]
}`

	var sawIn bool
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestBody: func(body []byte) {
			s := string(body)
			if strings.Contains(s, "image_url") && strings.Contains(s, `"type":"file"`) {
				sawIn = true
			}
		},
		NonStreamJSON: mmJSON,
	}))
	t.Cleanup(srv.Close)

	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")
	imgB64 := base64.StdEncoding.EncodeToString(png)
	pdfB64 := base64.StdEncoding.EncodeToString(pdf)
	dataImageURL := "data:image/png;base64," + imgB64

	parts := []openai.ChatCompletionContentPartUnionParam{
		openai.TextContentPart("describe attachments"),
		openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{URL: dataImageURL}),
		openai.FileContentPart(openai.ChatCompletionContentPartFileFileParam{
			FileData: openai.String(pdfB64),
			Filename: openai.String("minimal.pdf"),
		}),
	}

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	res, err := cli.CreateChatCompletion(context.Background(), openai.ChatCompletionNewParams{
		Model: shared.ChatModelGPT4oMini,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(parts),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawIn {
		t.Fatal("expected multimodal markers in request body")
	}
	if res.ID != "chatcmpl_mm_out" {
		t.Fatalf("id: got %q", res.ID)
	}
	if !strings.Contains(res.Choices[0].Message.Content, "multimodal-out") {
		t.Fatalf("message content: %q", res.Choices[0].Message.Content)
	}
}

func TestHandler_wrongPath_404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/other")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}
