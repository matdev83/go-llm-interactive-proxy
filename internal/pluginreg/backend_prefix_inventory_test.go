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
		if id == "bedrock" {
			continue
		}
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			var node yaml.Node
			if err := yaml.Unmarshal([]byte(standardBackendBuildYAML(id)), &node); err != nil {
				t.Fatal(err)
			}
			be, err := reg.BuildBackend(id, node, nil)
			if err != nil {
				t.Fatalf("BuildBackend(%q) error = %v", id, err)
			}
			if len(be.BackendPrefixes) == 0 {
				t.Fatalf("BuildBackend(%q) BackendPrefixes is empty", id)
			}
			if !slices.Contains(be.BackendPrefixes, id) {
				t.Fatalf("BuildBackend(%q) BackendPrefixes = %#v, want factory id %q", id, be.BackendPrefixes, id)
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
	case "acp", "anthropic", "openai-legacy", "openai-responses", "openrouter", "nvidia":
		return "base_url: http://127.0.0.1:9\n"
	case "gemini":
		return "api_key: test\n"
	case "ollama", "ollama-cloud":
		return "responses_api: disabled\n"
	case "bedrock":
		return "region: us-east-1\n"
	default:
		return ""
	}
}
