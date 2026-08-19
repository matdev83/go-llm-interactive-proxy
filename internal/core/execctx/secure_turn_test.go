package execctx

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

func TestWithSecureSessionTurnNilParent(t *testing.T) {
	t.Parallel()
	st := SecureSessionTurn{
		SessionID: domain.SessionID("s"),
		TurnID:    domain.TurnID("u"),
	}
	ctx := WithSecureSessionTurn(nil, st) //nolint:staticcheck // SA1012: exercise nil-parent → TODO() branch
	if ctx == nil {
		t.Fatal("expected non-nil context when parent is nil (nil parent uses context.TODO)")
	}
	got, ok := SecureSessionTurnFromContext(ctx)
	if !ok {
		t.Fatal("expected turn binding in context")
	}
	if got != st {
		t.Fatalf("turn: got %+v want %+v", got, st)
	}
}

func TestWithSecureSessionTurnProjectsContentFreeSDKPolicy(t *testing.T) {
	t.Parallel()

	ctx := WithSecureSessionTurn(nil, SecureSessionTurn{
		SessionID: domain.SessionID("s"),
		TurnID:    domain.TurnID("t"),
		Policy:    domain.PolicyMetadata{TranscriptEnabled: true},
	}) //nolint:staticcheck // SA1012: exercise nil-parent hardening
	got, ok := session.SecureTurnPolicyFromContext(ctx)
	if !ok || !got.TranscriptEnabled {
		t.Fatalf("public secure-turn policy = %+v ok=%v", got, ok)
	}
}
