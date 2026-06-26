package pluginreg

import (
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/opencodecommon"
)

func TestPrefixedModelIDsFromYAML_stripsNativePrefixAndFallsBackToCanonicalTail(t *testing.T) {
	t.Parallel()
	got, err := prefixedModelIDsFromYAML("openai-codex", modelInventoryYAML{Items: []modelInventoryItemYAML{
		{NativeID: "openai-codex/gpt-5.3-codex"},
		{CanonicalID: "openai-codex/gpt-5.4"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []prefixedModelYAML{{RawID: "gpt-5.3-codex"}, {RawID: "gpt-5.4"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestOpencodeModelEntriesFromYAML_usesSharedPrefixedParser(t *testing.T) {
	t.Parallel()
	got, err := opencodeModelEntriesFromYAML(opencodecommon.BackendGo, modelInventoryYAML{Items: []modelInventoryItemYAML{
		{NativeID: "opencode-go/kimi-k2.7-code", DisplayName: "Kimi"},
		{CanonicalID: "moonshot/kimi-k2.7-thinking"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []opencodecommon.ModelEntry{
		{RawID: "kimi-k2.7-code", DisplayName: "Kimi"},
		{RawID: "kimi-k2.7-thinking"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %#v, want %#v", got, want)
	}
}

func TestFirstAPIKeyReturnsResolvedKeysAndPrimary(t *testing.T) {
	t.Parallel()
	keys, primary := firstAPIKey("", []string{" yaml-1 "}, []hostedCredentialYAML{{APIKey: " cred-1 "}}, []string{"env-1"})
	if primary != "yaml-1" {
		t.Fatalf("primary = %q", primary)
	}
	want := []string{"yaml-1", "cred-1"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %#v, want %#v", keys, want)
	}
}
