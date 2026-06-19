package runtimebundle_test

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"

func testOpenAIBackendYAML() string {
	return `api_key: test-key
models:
  source: inline
  items:
    - canonical_id: openai/test-model
      native_id: test-model
      display_name: Test Model
`
}

func testModelInventory() modelinventory.Provider {
	return modelinventory.StaticProvider{
		Source: modelinventory.SourceStaticBuiltin,
		Models: []modelinventory.Model{{
			CanonicalID: "test/model",
			NativeID:    "model",
			DisplayName: "Test Model",
		}},
	}
}
