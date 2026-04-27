// Package httpauth defines transport-layer authentication contracts for the standard
// HTTP distribution (design §13, R4). HTTP types, [Provider], and [AuthErrorRenderer] live here;
// semantic auth DTOs remain in [github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth].
// Principal context is stored through [github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview]
// (same context key as [execview.WithPrincipal]).
//
// Errors: [Provider.Authenticate] errors may be logged by stdhttp integration. Implementations
// must return errors without secrets, bearer tokens, or other high-entropy credential material.
package httpauth
