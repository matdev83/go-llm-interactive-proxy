package execctx

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
)

// keySecureTurn is offset next to [keyViews] to avoid context key collisions.
const keySecureTurn ctxKey = 6201

// SecureSessionTurn carries validated secure-session identifiers for one prepared turn.
type SecureSessionTurn struct {
	SessionID domain.SessionID
	TurnID    domain.TurnID
	// Policy is the effective recording policy for this turn (transcript, audit, redaction).
	Policy domain.PolicyMetadata
}

// WithSecureSessionTurn attaches validated session and turn ids for downstream attempt recording.
// A nil parent is treated as [context.TODO] so the result is always non-nil.
func WithSecureSessionTurn(ctx context.Context, t SecureSessionTurn) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	return context.WithValue(ctx, keySecureTurn, t)
}

// SecureSessionTurnFromContext returns the turn binding attached with [WithSecureSessionTurn], if any.
func SecureSessionTurnFromContext(ctx context.Context) (SecureSessionTurn, bool) {
	if ctx == nil {
		return SecureSessionTurn{}, false
	}
	raw := ctx.Value(keySecureTurn)
	if raw == nil {
		return SecureSessionTurn{}, false
	}
	t, ok := raw.(SecureSessionTurn)
	return t, ok
}
