package openairesponses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/responses"
)

func TestEmitOutputMediaFromResponse_inputImageAndFile(t *testing.T) {
	t.Parallel()
	const raw = `{
  "id": "resp_mm",
  "object": "response",
  "status": "completed",
  "output": [
    {
      "type": "message",
      "id": "msg_mm",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "input_image", "image_url": "https://cdn.example.com/out.png"},
        {"type": "input_file", "file_id": "file_doc_1"}
      ]
    }
  ]
}`
	var resp responses.Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	s := &sdkStream{}
	emitOutputMediaFromResponse(s, resp)
	evs := stream.DrainPending(&s.pending)
	var media []lipapi.Event
	for _, ev := range evs {
		if ev.Kind == lipapi.EventAssistantImageRef || ev.Kind == lipapi.EventAssistantFileRef {
			media = append(media, ev)
		}
	}
	if len(media) != 2 {
		t.Fatalf("assistant media events: %+v (all %d)", media, len(evs))
	}
	if media[0].Kind != lipapi.EventAssistantImageRef || media[0].AssistantRef != "https://cdn.example.com/out.png" {
		t.Fatalf("image event: %+v", media[0])
	}
	if media[1].Kind != lipapi.EventAssistantFileRef || media[1].AssistantRef != "file_doc_1" {
		t.Fatalf("file event: %+v", media[1])
	}
}

func TestHandleUnion_completed_emitsAssistantMediaAfterText(t *testing.T) {
	t.Parallel()
	raw := `{
  "type": "response.completed",
  "sequence_number": 1,
  "response": {
    "id": "resp_x",
    "object": "response",
    "status": "completed",
    "output": [
      {
        "type": "message",
        "id": "m1",
        "status": "completed",
        "role": "assistant",
        "content": [
          {"type": "output_text", "text": "see"},
          {"type": "input_image", "image_url": {"url": "https://img.example/x.png"}}
        ]
      }
    ]
  }
}`
	var u responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &sdkStream{}
	s.handleUnion(u)
	evs := stream.DrainPending(&s.pending)
	var gotText strings.Builder
	var img string
	for _, ev := range evs {
		switch ev.Kind {
		case lipapi.EventTextDelta:
			gotText.WriteString(ev.Delta)
		case lipapi.EventAssistantImageRef:
			img = ev.AssistantRef
		}
	}
	if got := gotText.String(); got != "see" {
		t.Fatalf("text %q", got)
	}
	if img != "https://img.example/x.png" {
		t.Fatalf("image ref %q", img)
	}
}
