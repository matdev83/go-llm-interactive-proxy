package policy

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

// WithSecureTurn is a test-only helper for the trusted secure-session policy seam.
func WithSecureTurn(ctx context.Context, transcriptEnabled bool) context.Context {
	return session.WithSecureTurnPolicy(ctx, session.SecureTurnPolicyView{TranscriptEnabled: transcriptEnabled})
}
