package httpauth

import (
	"context"
	"net/http"
)

// Provider performs transport-native authentication before frontend decode (R4).
// Returning a non-nil error is fail-closed for that provider. Implementations must not
// write to w unless returning TypeReject or TypeChallenge (or annotate-only paths that
// only add headers via the result, applied by the stdhttp integration layer).
type Provider interface {
	Authenticate(ctx context.Context, w http.ResponseWriter, r *http.Request) (AuthenticationResult, error)
}
