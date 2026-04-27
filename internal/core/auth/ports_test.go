package auth

import (
	"context"
	"testing"

	sdkauth "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
)

type stubAuthenticator struct{}

func (stubAuthenticator) Authenticate(_ context.Context, _ sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	return sdkauth.Decision{
		Outcome:   sdkauth.OutcomeAllow,
		Principal: execview.PrincipalView{ID: "ok"},
	}, nil
}

type denyingRemoteStub struct{}

func (denyingRemoteStub) Decide(_ context.Context, _ sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "test-deny"}, nil
}

func TestAuthenticator_interface(t *testing.T) {
	t.Parallel()
	var _ Authenticator = stubAuthenticator{}
}

func TestRemoteDecider_interface(t *testing.T) {
	t.Parallel()
	var _ RemoteDecider = denyingRemoteStub{}
}
