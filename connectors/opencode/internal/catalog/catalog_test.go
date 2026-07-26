package catalog

import (
	"errors"
	"strings"
	"testing"
)

func TestModelCatalog_resolveCanonicalAndNative(t *testing.T) {
	t.Parallel()

	catalog := NewModelCatalog(BackendGo, []ModelEntry{
		{RawID: "kimi-k2.7-code"},
		{RawID: "minimax-m3", Endpoint: "https://example.test/v1/messages", AISDKPackage: "@ai-sdk/anthropic"},
	}, testKeywordFallbackResolver())

	res, err := catalog.Resolve("moonshotai/kimi-k2.7-code")
	if err != nil {
		t.Fatal(err)
	}
	if res.WireModel != "kimi-k2.7-code" || res.Flavor != FlavorOpenAIChat {
		t.Fatalf("resolve canonical = %+v", res)
	}

	res, err = catalog.Resolve("opencode-go/kimi-k2.7-code")
	if err != nil {
		t.Fatal(err)
	}
	if res.WireModel != "kimi-k2.7-code" {
		t.Fatalf("resolve native = %+v", res)
	}

	res, err = catalog.Resolve("minimax/minimax-m3")
	if err != nil {
		t.Fatal(err)
	}
	if res.Flavor != FlavorAnthropicMessages {
		t.Fatalf("minimax flavor = %q", res.Flavor)
	}
}

func TestModelCatalog_unknownModelFailsExplicitly(t *testing.T) {
	t.Parallel()

	catalog := NewModelCatalog(BackendZen, []ModelEntry{{RawID: "gpt-5.4"}}, testKeywordFallbackResolver())
	_, err := catalog.Resolve("unknown/vendor-model")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrUnknownModel) && !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("err = %v", err)
	}
}
