package openairesponses_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	front "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	refcli "github.com/matdev83/go-llm-interactive-proxy/internal/refclient/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

func TestIntegration_refclientNonStreaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "integration-ok", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
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
	if !strings.Contains(res.Output[0].Content[0].Text, "integration-ok") {
		t.Fatalf("output: %+v", res.Output)
	}
}

func TestIntegration_refclientStreaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "stream-ok", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
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
		if cur.Type != "response.completed" {
			continue
		}
		raw, err := json.Marshal(cur.Response)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "stream-ok") {
			saw = true
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if !saw {
		t.Fatal("expected response.completed with stream-ok")
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
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
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
	_, err := cli.CreateResponse(context.Background(), responses.ResponseNewParams{
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
	v, ok := capture.Load("last")
	if !ok {
		t.Fatal("expected captured call")
	}
	call := v.(lipapi.Call)
	parts := call.Messages[0].Parts
	if len(parts) < 3 {
		t.Fatalf("parts: %+v", parts)
	}
	if parts[1].Kind != lipapi.PartImageRef {
		t.Fatalf("want image part, got %+v", parts[1])
	}
	if parts[2].Kind != lipapi.PartFileRef || parts[2].FileMIME != "application/pdf" {
		t.Fatalf("want pdf file part, got %+v", parts[2])
	}
}

func TestIntegration_malformedJSON_returns400(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "x", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	res, err := http.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(`{`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}
}
