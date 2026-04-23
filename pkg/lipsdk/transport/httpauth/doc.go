// Package httpauth defines transport-layer authentication contracts for the standard
// HTTP distribution (design §13, R4). HTTP types and [Provider] stay here; principal
// context is stored through [github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview]
// (same context key as [execview.WithPrincipal]).
package httpauth
