package anthropicmessages_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/anthropicmessages"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
)

func TestCreateMessage_nonStreaming(t *testing.T) {
	t.Parallel()
	const okBody = `{
  "id": "msg_01",
  "type": "message",
  "role": "assistant",
  "model": "claude-3-5-haiku-20241022",
  "content": [{"type":"text","text":"hello"}],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("x-api-key") == "" {
			t.Error("expected x-api-key header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okBody))
	}))
	t.Cleanup(srv.Close)

	cli := anthropicmessages.New(anthropicmessages.Config{
		BaseURL: srv.URL,
		APIKey:  "sk-ant-test",
	})
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
	if msg.ID != "msg_01" {
		t.Fatalf("id: %q", msg.ID)
	}
}

func TestCreateMessage_multimodal_imageAndPDF(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s := string(b)
		if !strings.Contains(s, `"type":"image"`) || !strings.Contains(s, `"type":"document"`) {
			t.Fatalf("expected image and document blocks in body: %s", s)
		}
		const okBody = `{"id":"msg_mm","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okBody))
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

	cli := anthropicmessages.New(anthropicmessages.Config{BaseURL: srv.URL, APIKey: "k"})
	_, err := cli.CreateMessage(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("claude-3-5-haiku-20241022"),
		MaxTokens: 128,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("describe"), img, doc),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCreateMessage_httpError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`))
	}))
	t.Cleanup(srv.Close)

	cli := anthropicmessages.New(anthropicmessages.Config{BaseURL: srv.URL, APIKey: "k"})
	_, err := cli.CreateMessage(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("claude-3-5-haiku-20241022"),
		MaxTokens: 8,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("x"))},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var api *anthropic.Error
	if !errors.As(err, &api) {
		t.Fatalf("expected anthropic.Error, got %T: %v", err, err)
	}
}

func TestCreateMessageStream_readsEvents(t *testing.T) {
	t.Parallel()
	start := `{"type":"message_start","message":{"id":"msg_s","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}`
	stop := `{"type":"message_stop"}`
	var body strings.Builder
	fmt.Fprintf(&body, "event: message_start\ndata: %s\n\n", start)
	fmt.Fprintf(&body, "event: message_stop\ndata: %s\n\n", stop)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body.String())
	}))
	t.Cleanup(srv.Close)

	cli := anthropicmessages.New(anthropicmessages.Config{BaseURL: srv.URL, APIKey: "k"})
	stream := cli.CreateMessageStream(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("claude-3-5-haiku-20241022"),
		MaxTokens: 16,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("s"))},
	})
	var sawStart, sawStop bool
	for stream.Next() {
		ev := stream.Current()
		switch ev.Type {
		case "message_start":
			sawStart = true
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
