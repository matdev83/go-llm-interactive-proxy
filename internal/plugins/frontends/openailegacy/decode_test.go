package openailegacy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join(refclienttest.ModuleRoot(t), "testdata", "openailegacy_frontend", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeChat_textNonStream(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_nonstream.json")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Stream {
		t.Fatal("expected stream false")
	}
	if d.Model != "gpt-4o-mini" {
		t.Fatalf("model %q", d.Model)
	}
	if openailegacy.ModelFromCall(d.Call) != "gpt-4o-mini" {
		t.Fatal("model extension")
	}
	if got := d.Call.Route.Selector; got != "stub:gpt-4o-mini" {
		t.Fatalf("route %q", got)
	}
	if len(d.Call.Messages) != 1 || d.Call.Messages[0].Role != lipapi.RoleUser {
		t.Fatalf("messages: %+v", d.Call.Messages)
	}
	if len(d.Call.Messages[0].Parts) != 1 || d.Call.Messages[0].Parts[0].Kind != lipapi.PartText {
		t.Fatalf("parts: %+v", d.Call.Messages[0].Parts)
	}
	if d.Call.Messages[0].Parts[0].Text != "ping" {
		t.Fatalf("text %q", d.Call.Messages[0].Parts[0].Text)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeChat_textStream(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_stream.json")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Stream {
		t.Fatal("expected stream true")
	}
}

func TestDecodeChat_multimodal(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_multimodal_nonstream.json")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := d.Call.Messages[0].Parts
	if len(parts) != 3 {
		t.Fatalf("want 3 parts, got %d", len(parts))
	}
	if parts[0].Kind != lipapi.PartText || parts[0].Text != "describe attachments" {
		t.Fatalf("part0: %+v", parts[0])
	}
	if parts[1].Kind != lipapi.PartImageRef || !strings.Contains(parts[1].ImageRef, "base64,") {
		t.Fatalf("part1: %+v", parts[1])
	}
	if parts[1].ImageMIME != "image/png" {
		t.Fatalf("image mime %q", parts[1].ImageMIME)
	}
	if parts[2].Kind != lipapi.PartFileRef || parts[2].FileMIME != "application/pdf" {
		t.Fatalf("part2: %+v", parts[2])
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeChat_requiresRoute(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_nonstream.json")
	_, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: ""})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeChat_invalidJSON(t *testing.T) {
	t.Parallel()
	_, err := openailegacy.DecodeChatRequest([]byte("{"), openailegacy.DecodeOptions{
		RouteSelector: "stub:m",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}
