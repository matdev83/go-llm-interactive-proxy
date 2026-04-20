// Package refbackend provides spec-shaped HTTP emulators for remote inference APIs.
// They are test-support utilities only: they must not be imported from the core runtime
// (internal/core) or from protocol codec plugins (internal/plugins).
//
// Subpackages include openairesponses, openaichat, anthropicmessages, gemini, bedrock, and acp.
// Each implements the server-side wire shapes that official vendor clients expect,
// so integration tests can validate backend connectors without live providers.
// Normative contracts are documented in .kiro/specs/go-core-reimplementation-v1/research.md.
package refbackend
