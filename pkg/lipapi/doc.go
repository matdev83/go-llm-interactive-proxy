// Package lipapi defines the canonical public contracts shared across frontends,
// backends, and future external integrations.
//
// Tool-call and assistant history (requirements 8.x): only a documented subset of
// provider-specific tool history is round-tripped through Message and Part values today.
// OpenAI Chat and OpenAI Responses frontends implement the supported shapes; other
// frontends may ignore or normalize unsupported tool rows. See frontend package docs
// next to each adapter for the exact supported subset per protocol.
package lipapi
