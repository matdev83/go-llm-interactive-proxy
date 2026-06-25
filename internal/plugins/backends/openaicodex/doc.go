// Package openaicodex implements the OpenAI Codex Responses backend connector using
// stdlib HTTP and a local SSE parser. It maps lipapi.Call to Codex /responses JSON
// and streams canonical events from the Codex SSE wire format.
package openaicodex

// ID is the reserved plugin identifier for the OpenAI Codex backend.
const ID = "openai-codex"
