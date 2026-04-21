package openaiwire

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestMustJSON_roundTrip(t *testing.T) {
	t.Parallel()
	blk := map[string]json.RawMessage{
		"type": json.RawMessage(`"text"`),
		"text": json.RawMessage(`"hi"`),
	}
	var s struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(MustJSON(blk), &s); err != nil {
		t.Fatal(err)
	}
	if s.Type != "text" || s.Text != "hi" {
		t.Fatalf("%+v", s)
	}
}

func TestImagePartFromURL_dataURL(t *testing.T) {
	t.Parallel()
	p, err := ImagePartFromURL("data:image/png;base64,abcd")
	if err != nil {
		t.Fatal(err)
	}
	if p.ImageMIME != "image/png" {
		t.Fatalf("mime %q", p.ImageMIME)
	}
}

func TestFilePartFromBase64_pdfFilename(t *testing.T) {
	t.Parallel()
	p := FilePartFromBase64("doc.PDF", "qqq")
	if p.FileMIME != "application/pdf" {
		t.Fatalf("mime %q", p.FileMIME)
	}
	if p.Kind != lipapi.PartFileRef {
		t.Fatalf("kind %v", p.Kind)
	}
}
