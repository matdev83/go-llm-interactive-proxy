package httpauth

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
)

type ctxKey int

const keyPrincipal ctxKey = iota + 18300

// WithPrincipal returns a child context carrying the canonical principal for downstream
// handlers and the execution pipeline. A nil parent is treated as [context.Background] so the
// result is always non-nil and safe for functions that do not allow nil [context.Context].
func WithPrincipal(ctx context.Context, p execview.PrincipalView) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, keyPrincipal, p)
}

// PrincipalFromContext returns the principal set by transport auth, if any.
func PrincipalFromContext(ctx context.Context) (execview.PrincipalView, bool) {
	if ctx == nil {
		return execview.PrincipalView{}, false
	}
	raw := ctx.Value(keyPrincipal)
	p, ok := raw.(execview.PrincipalView)
	return p, ok
}
