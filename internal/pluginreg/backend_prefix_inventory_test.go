package pluginreg

import (
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestStandardBackends_exposeInventoryPrefixes(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	for _, id := range standardBackendFactoryIDs(t) {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			var node yaml.Node
			if err := yaml.Unmarshal([]byte(standardBackendBuildYAML(id)), &node); err != nil {
				t.Fatal(err)
			}
			be, err := reg.BuildBackend(id, node, nil, BackendFactoryDeps{})
			if err != nil {
				t.Fatalf("BuildBackend(%q) error = %v", id, err)
			}
			if len(be.BackendPrefixes) == 0 {
				t.Fatalf("BuildBackend(%q) BackendPrefixes is empty", id)
			}
			wantPrefix := standardBackendWantPrefix(id)
			if !slices.Contains(be.BackendPrefixes, wantPrefix) {
				t.Fatalf("BuildBackend(%q) BackendPrefixes = %#v, want prefix %q", id, be.BackendPrefixes, wantPrefix)
			}
			for _, prefix := range be.BackendPrefixes {
				prefix = strings.TrimSpace(prefix)
				if prefix == "" || strings.Contains(prefix, "/") || strings.Contains(prefix, ":") {
					t.Fatalf("BuildBackend(%q) invalid prefix %q", id, prefix)
				}
			}
		})
	}
}

func TestReservedStandardBackendPrefixes_coverStandardBackendPrefixes(t *testing.T) {
	t.Parallel()

	for _, id := range standardBackendFactoryIDs(t) {
		if IsCustomCompatibleBackendKind(id) {
			continue
		}
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			var node yaml.Node
			if err := yaml.Unmarshal([]byte(standardBackendBuildYAML(id)), &node); err != nil {
				t.Fatal(err)
			}
			reg := NewRegistry()
			if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
				t.Fatal(err)
			}
			be, err := reg.BuildBackend(id, node, nil, BackendFactoryDeps{})
			if err != nil {
				t.Fatalf("BuildBackend(%q) error = %v", id, err)
			}
			for _, prefix := range be.BackendPrefixes {
				if _, ok := reservedStandardBackendPrefixes[prefix]; !ok {
					t.Fatalf("standard backend %q exposes prefix %q not reserved for custom connectors", id, prefix)
				}
			}
		})
	}
}

func standardBackendFactoryIDs(t *testing.T) []string {
	t.Helper()
	be := StandardBackendBundle(UpstreamAPIKeys{})
	out := make([]string, 0, len(be.Backends))
	for _, entry := range be.Backends {
		out = append(out, entry.ID)
	}
	slices.Sort(out)
	return out
}

func standardBackendBuildYAML(id string) string {
	switch id {
	case "acp", "anthropic", "openai-legacy", "openai-responses", "openrouter", "nvidia", "opencode-go", "opencode-zen":
		return "base_url: http://127.0.0.1:9\n"
	case "openai-codex":
		return "base_url: http://127.0.0.1:9\naccess_token: test\n"
	case "gemini":
		return "api_key: test\n"
	case "ollama", "ollama-cloud":
		return "responses_api: disabled\n"
	case "bedrock":
		return "region: us-east-1\n"
	case CustomOpenAILegacyCompatibleID:
		return "backend_prefix: custom-openai-legacy\nbase_url: http://127.0.0.1:9/v1\n"
	case CustomOpenAIResponsesCompatibleID:
		return "backend_prefix: custom-openai-responses\nbase_url: http://127.0.0.1:9/v1\n"
	case CustomAnthropicCompatibleID:
		return "backend_prefix: custom-anthropic\nbase_url: http://127.0.0.1:9\n"
	default:
		return ""
	}
}

func standardBackendWantPrefix(id string) string {
	switch id {
	case CustomOpenAILegacyCompatibleID:
		return "custom-openai-legacy"
	case CustomOpenAIResponsesCompatibleID:
		return "custom-openai-responses"
	case CustomAnthropicCompatibleID:
		return "custom-anthropic"
	default:
		return id
	}
}
