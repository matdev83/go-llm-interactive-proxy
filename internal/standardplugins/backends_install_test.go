package standardplugins

import (
	"reflect"
	"testing"
)

func TestPrefixedModelIDsFromYAML_stripsNativePrefixAndFallsBackToCanonicalTail(t *testing.T) {
	t.Parallel()
	got, err := prefixedModelIDsFromYAML("openai-responses", modelInventoryYAML{Items: []modelInventoryItemYAML{
		{NativeID: "openai-responses/gpt-5.3-codex-spark"},
		{CanonicalID: "openai-responses/gpt-5.4"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []prefixedModelYAML{{RawID: "gpt-5.3-codex-spark"}, {RawID: "gpt-5.4"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
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
