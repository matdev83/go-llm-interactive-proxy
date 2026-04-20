// Package openairesponses implements the OpenAI Responses–compatible HTTP frontend:
// JSON decode to lipapi.Call, core execution, and JSON or SSE encode for official clients.
// Routing uses header X-LIP-Route (selector like backend:model) until task 11 wires config.
package openairesponses

// ID is the reserved plugin identifier for the OpenAI Responses frontend.
const ID = "openai-responses"
