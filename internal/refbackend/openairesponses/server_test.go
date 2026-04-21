package openairesponses_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openairesponses"
	refcli "github.com/matdev83/go-llm-interactive-proxy/internal/refclient/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

func TestHandler_nonStreaming_refclientSmoke(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	res, err := cli.CreateResponse(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("gpt-4o-mini"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage("ping", responses.EasyInputMessageRoleUser),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "resp_refbackend_1" {
		t.Fatalf("response id: got %q", res.ID)
	}
	if len(res.Output) == 0 || res.Output[0].Content[0].Text != "ok" {
		t.Fatalf("output: %+v", res.Output)
	}
}

func TestHandler_streaming_refclientReadsCompleted(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	stream := cli.CreateResponseStream(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("gpt-4o-mini"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage("hi", responses.EasyInputMessageRoleUser),
			},
		},
	})
	var saw bool
	for stream.Next() {
		cur := stream.Current()
		if cur.Type == "response.completed" && cur.Response.ID == "resp_refbackend_stream" {
			saw = true
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if !saw {
		t.Fatal("expected response.completed with id resp_refbackend_stream")
	}
}

func TestHandler_requiresBearer(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: ""})
	_, err := cli.CreateResponse(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("gpt-4o-mini"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage("x", responses.EasyInputMessageRoleUser),
			},
		},
	})
	if err == nil {
		t.Fatal("expected error without API key / bearer")
	}
}

func TestHandler_multimodalRequest_customMultimodalResponseJSON(t *testing.T) {
	t.Parallel()
	const mmJSON = `{
  "id": "resp_mm_out",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [
    {
      "type": "message",
      "id": "m1",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "output_text", "text": "multimodal-out:image+pdf"}
      ]
    }
  ]
}`

	var sawIn bool
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestBody: func(body []byte) {
			s := string(body)
			if strings.Contains(s, "input_image") && strings.Contains(s, "input_file") {
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

	img := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
	img.OfInputImage.ImageURL = openai.String(dataImageURL)

	filePart := responses.ResponseInputContentUnionParam{
		OfInputFile: &responses.ResponseInputFileParam{
			FileData: openai.String(pdfB64),
			Filename: openai.String("minimal.pdf"),
		},
	}

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	res, err := cli.CreateResponse(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("gpt-4o-mini"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfInputMessage(
					responses.ResponseInputMessageContentListParam{
						responses.ResponseInputContentParamOfInputText("describe attachments"),
						img,
						filePart,
					},
					"user",
				),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawIn {
		t.Fatal("expected multimodal markers in request body")
	}
	if res.ID != "resp_mm_out" {
		t.Fatalf("id: got %q", res.ID)
	}
	if !strings.Contains(res.Output[0].Content[0].Text, "multimodal-out") {
		t.Fatalf("output text: %q", res.Output[0].Content[0].Text)
	}
}

func TestHandler_assistantOutput_imageAndFileInMessage_refclientParse(t *testing.T) {
	t.Parallel()
	const mmOut = `{
  "id": "resp_asst_mm",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [
    {
      "type": "message",
      "id": "m_asst",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "output_text", "text": "here"},
        {"type": "input_image", "image_url": "https://cdn.example.com/ref-out.png"},
        {"type": "input_file", "file_id": "file-ref-1"}
      ]
    }
  ]
}`
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		NonStreamJSON: mmOut,
	}))
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	res, err := cli.CreateResponse(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("gpt-4o-mini"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage("x", responses.EasyInputMessageRoleUser),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Output) != 1 || res.Output[0].Type != "message" {
		t.Fatalf("output: %+v", res.Output)
	}
	msg := res.Output[0].AsMessage()
	if len(msg.Content) < 3 {
		t.Fatalf("content blocks: %d", len(msg.Content))
	}
	if msg.Content[0].Type != "output_text" || msg.Content[0].Text != "here" {
		t.Fatalf("text block: %+v", msg.Content[0])
	}
	// Wire-level assistant media: SDK preserves JSON on union; backend maps via RawJSON.
	if msg.Content[1].RawJSON() == "" || !strings.Contains(msg.Content[1].RawJSON(), "input_image") {
		t.Fatalf("image content raw: %q", msg.Content[1].RawJSON())
	}
	if msg.Content[2].RawJSON() == "" || !strings.Contains(msg.Content[2].RawJSON(), "file-ref-1") {
		t.Fatalf("file content raw: %q", msg.Content[2].RawJSON())
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
