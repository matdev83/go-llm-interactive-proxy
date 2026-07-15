package stdhttp

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
)

// openAIModelList aliases the core OpenAI list shape for local tests/handlers.
type openAIModelList = modelregistry.OpenAIModelList

type openAIModel = modelregistry.OpenAIModel

func buildOpenAIModelsList(models []modelregistry.BackendModel) openAIModelList {
	return modelregistry.BuildOpenAIModelsList(models)
}
