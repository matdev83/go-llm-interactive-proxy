package gemini_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/gemini"
	refcli "github.com/matdev83/go-llm-interactive-proxy/internal/refclient/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
	"google.golang.org/genai"
)

func TestHandler_nonStreaming_refclientSmoke(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	cli, err := refcli.New(context.Background(), refcli.Config{
		APIKey:     "fake-key",
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := cli.GenerateContent(context.Background(), "gemini-2.0-flash", []*genai.Content{
		genai.NewContentFromText("ping", genai.RoleUser),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Candidates) == 0 || out.Candidates[0].Content == nil {
		t.Fatalf("candidates: %+v", out.Candidates)
	}
	parts := out.Candidates[0].Content.Parts
	if len(parts) == 0 || parts[0].Text != "ok" {
		t.Fatalf("parts: %+v", parts)
	}
}

func TestHandler_streaming_refclientReadsChunk(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	cli, err := refcli.New(context.Background(), refcli.Config{
		APIKey:     "k",
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for res, serr := range cli.GenerateContentStream(context.Background(), "gemini-2.0-flash",
		[]*genai.Content{genai.NewContentFromText("s", genai.RoleUser)}, nil) {
		if serr != nil {
			t.Fatal(serr)
		}
		for _, c := range res.Candidates {
			if c.Content != nil {
				for _, p := range c.Content.Parts {
					got += p.Text
				}
			}
		}
	}
	if got != "stream-ok" {
		t.Fatalf("stream text: %q", got)
	}
}

func TestHandler_requiresAPIKey(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	// genai.New rejects an empty API key before any request; exercise the handler directly.
	resp, err := http.Post(srv.URL+"/v1beta/models/gemini-2.0-flash:generateContent", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without x-goog-api-key: %d", resp.StatusCode)
	}
}

func TestHandler_multimodalRequest_customJSON(t *testing.T) {
	t.Parallel()
	const mmJSON = `{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [{"text": "multimodal-out:image+pdf"}]
      }
    }
  ]
}`

	var sawIn bool
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestBody: func(body []byte) {
			s := string(body)
			if strings.Contains(s, "inlineData") || strings.Contains(s, "inline_data") {
				sawIn = true
			}
		},
		NonStreamJSON: mmJSON,
	}))
	t.Cleanup(srv.Close)

	cli, err := refcli.New(context.Background(), refcli.Config{
		APIKey:     "k",
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")
	contents := []*genai.Content{{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{Text: "describe"},
			{InlineData: &genai.Blob{MIMEType: "image/png", Data: png}},
			{InlineData: &genai.Blob{MIMEType: "application/pdf", Data: pdf}},
		},
	}}

	out, err := cli.GenerateContent(context.Background(), "gemini-2.0-flash", contents, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !sawIn {
		t.Fatal("expected inline image/pdf payload in request body")
	}
	if out.Candidates[0].Content.Parts[0].Text != "multimodal-out:image+pdf" {
		t.Fatalf("output text: %q", out.Candidates[0].Content.Parts[0].Text)
	}
}

func TestHandler_multimodalResponse_inlineImage(t *testing.T) {
	t.Parallel()
	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")
	imgB64 := base64.StdEncoding.EncodeToString(png)
	pdfB64 := base64.StdEncoding.EncodeToString(pdf)
	mmJSON := `{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [
          {"text": "here-is-image"},
          {"inlineData": {"mimeType": "image/png", "data": "` + imgB64 + `"}},
          {"inlineData": {"mimeType": "application/pdf", "data": "` + pdfB64 + `"}}
        ]
      }
    }
  ]
}`

	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		NonStreamJSON: mmJSON,
	}))
	t.Cleanup(srv.Close)

	cli, err := refcli.New(context.Background(), refcli.Config{
		APIKey:     "k",
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := cli.GenerateContent(context.Background(), "gemini-2.0-flash", []*genai.Content{
		genai.NewContentFromText("show", genai.RoleUser),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	parts := out.Candidates[0].Content.Parts
	if len(parts) != 3 || parts[0].Text != "here-is-image" {
		t.Fatalf("parts: %+v", parts)
	}
	if parts[1].InlineData == nil || parts[1].InlineData.MIMEType != "image/png" {
		t.Fatalf("inline part: %+v", parts[1])
	}
	if string(parts[1].InlineData.Data) != string(png) {
		t.Fatalf("inline bytes len got %d want %d", len(parts[1].InlineData.Data), len(png))
	}
	if parts[2].InlineData == nil || parts[2].InlineData.MIMEType != "application/pdf" {
		t.Fatalf("pdf inline part: %+v", parts[2])
	}
	if string(parts[2].InlineData.Data) != string(pdf) {
		t.Fatalf("pdf bytes len got %d want %d", len(parts[2].InlineData.Data), len(pdf))
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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}
