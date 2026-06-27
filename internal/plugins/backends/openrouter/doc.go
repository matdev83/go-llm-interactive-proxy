// Package openrouter implements the OpenRouter backend connector supporting both
// Chat Completions (/api/v1/chat/completions) and Responses (/api/v1/responses)
// upstream flavors. It maps lipapi.Call to the selected flavor using openai-go v3,
// injecting OpenRouter-specific headers and body fields via SDK request options.
//
// A route selector query param `?provider=<slug>` (e.g.
// `openrouter:deepseek/deepseek-r1?provider=deepinfra/turbo`) is translated into the
// upstream provider body field `{"order":["<slug>"],"allow_fallbacks":false}`. An
// explicit client body provider captured under openrouterwire.ExtProvider takes
// precedence over the route param.
package openrouter

// ID is the reserved plugin identifier for the OpenRouter backend.
const ID = "openrouter"
