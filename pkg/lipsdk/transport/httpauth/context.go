package httpauth

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// WithPrincipal is a transport-named alias for [execview.WithPrincipal] so the auth middleware
// and the policy core share one context key without the core importing this package.
func WithPrincipal(ctx context.Context, p execview.PrincipalView) context.Context {
	return execview.WithPrincipal(ctx, p)
}

// PrincipalFromContext is a transport-named alias for [execview.PrincipalFromContext].
func PrincipalFromContext(ctx context.Context) (execview.PrincipalView, bool) {
	return execview.PrincipalFromContext(ctx)
}

// WithScope is a transport-named alias for [scope.WithScope] so transport auth and the core
// share one context key for the authoritative principal/scope snapshot.
func WithScope(ctx context.Context, v scope.PrincipalScopeView) context.Context {
	return scope.WithScope(ctx, v)
}

// ScopeFromContext is a transport-named alias for [scope.ScopeFromContext].
func ScopeFromContext(ctx context.Context) (scope.PrincipalScopeView, bool) {
	return scope.ScopeFromContext(ctx)
}
