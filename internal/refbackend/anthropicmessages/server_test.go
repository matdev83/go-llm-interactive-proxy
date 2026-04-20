package anthropicmessages_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/anthropicmessages"
	refcli "github.com/matdev83/go-llm-interactive-proxy/internal/refclient/anthropicmessages"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
)

func TestHandler_nonStreaming_refclientSmoke(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	msg, err := cli.CreateMessage(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("claude-3-5-haiku-20241022"),
		MaxTokens: 64,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("ping")),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.ID != "msg_refbackend_1" {
		t.Fatalf("message id: got %q", msg.ID)
	}
	if len(msg.Content) == 0 || msg.Content[0].Text != "ok" {
		t.Fatalf("content: %+v", msg.Content)
	}
}

func TestHandler_streaming_refclientReadsStartStop(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	stream := cli.CreateMessageStream(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("claude-3-5-haiku-20241022"),
		MaxTokens: 16,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("hi")),
		},
	})
	var sawStart, sawStop bool
	for stream.Next() {
		cur := stream.Current()
		switch cur.Type {
		case "message_start":
			sawStart = true
			if cur.Message.ID != "msg_refbackend_stream" {
				t.Fatalf("message_start id: got %q", cur.Message.ID)
			}
		case "message_stop":
			sawStop = true
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawStart || !sawStop {
		t.Fatalf("events: start=%v stop=%v", sawStart, sawStop)
	}
}

func TestHandler_requiresAPIKey(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL, APIKey: ""})
	_, err := cli.CreateMessage(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("claude-3-5-haiku-20241022"),
		MaxTokens: 8,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("x"))},
	})
	if err == nil {
		t.Fatal("expected error without API key")
	}
}

func TestHandler_multimodalRequest_customJSON(t *testing.T) {
	t.Parallel()
	const mmJSON = `{
  "id": "msg_mm_out",
  "type": "message",
  "role": "assistant",
  "model": "claude-3-5-haiku-20241022",
  "content": [{"type":"text","text":"multimodal-out:image+pdf"}],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`

	var sawIn bool
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestBody: func(body []byte) {
			s := string(body)
			if strings.Contains(s, `"type":"image"`) && strings.Contains(s, `"type":"document"`) {
				sawIn = true
			}
		},
		NonStreamJSON: mmJSON,
	}))
	t.Cleanup(srv.Close)

	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")

	img := anthropic.NewImageBlock(anthropic.Base64ImageSourceParam{
		Data:      base64.StdEncoding.EncodeToString(png),
		MediaType: anthropic.Base64ImageSourceMediaTypeImagePNG,
	})
	doc := anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
		Data: base64.StdEncoding.EncodeToString(pdf),
	})

	cli := refcli.New(refcli.Config{BaseURL: srv.URL, APIKey: "sk-ant-test"})
	msg, err := cli.CreateMessage(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("claude-3-5-haiku-20241022"),
		MaxTokens: 128,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("describe attachments"), img, doc),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawIn {
		t.Fatal("expected image and document blocks in request body")
	}
	if msg.ID != "msg_mm_out" {
		t.Fatalf("id: got %q", msg.ID)
	}
	if msg.Content[0].Text != "multimodal-out:image+pdf" {
		t.Fatalf("output text: %q", msg.Content[0].Text)
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
