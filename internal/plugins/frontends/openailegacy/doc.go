// Package openailegacy implements the legacy OpenAI Chat Completions–compatible HTTP frontend:
// JSON decode to lipapi.Call, core execution, and JSON or SSE encode for official clients.
// Routing uses header X-LIP-Route (selector like backend:model) until task 11 wires config.
// Package openailegacy implements the legacy OpenAI Chat Completions–compatible HTTP frontend:
// POST /v1/chat/completions (decode → executor → encode).
package openailegacy

// ID is the reserved plugin identifier for the legacy OpenAI-compatible frontend.
const ID = "openai-legacy"
