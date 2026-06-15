// Package openrouter implements the OpenRouter backend connector supporting both
// Chat Completions (/api/v1/chat/completions) and Responses (/api/v1/responses)
// upstream flavors. It maps lipapi.Call to the selected flavor using openai-go v3,
// injecting OpenRouter-specific headers and body fields via SDK request options.
package openrouter

// ID is the reserved plugin identifier for the OpenRouter backend.
const ID = "openrouter"
