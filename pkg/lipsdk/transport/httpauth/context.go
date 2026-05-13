package httpauth

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
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
