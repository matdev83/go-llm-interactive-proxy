package execview

import "context"

type ctxKey int

// offset avoids collision with other context values in the same process (aligned with previous httpauth key range).
const keyPrincipal ctxKey = iota + 18300

// WithPrincipal returns a child context carrying the canonical principal for downstream
// handlers and the execution pipeline. A nil parent is treated as [context.TODO] so the
// result is always non-nil and safe for functions that do not allow nil [context.Context].
//
// Transport auth sets this at the edge; the policy core reads the same value without importing
// transport packages (introduce-hexagonal-architecture task 4.1).
func WithPrincipal(ctx context.Context, p PrincipalView) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	return context.WithValue(ctx, keyPrincipal, p)
}

// PrincipalFromContext returns the principal attached with [WithPrincipal], if any.
func PrincipalFromContext(ctx context.Context) (PrincipalView, bool) {
	if ctx == nil {
		return PrincipalView{}, false
	}
	raw := ctx.Value(keyPrincipal)
	p, ok := raw.(PrincipalView)
	return p, ok
}
