package auth

import (
	"context"
	"strings"

	sdkauth "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
)

// LocalNoOpAuthenticator grants credential-free access with an explicit non-anonymous
// principal derived from [OSIdentityProvider]. It must only be wired when access posture
// validation permits local no-op.
type LocalNoOpAuthenticator struct {
	OS OSIdentityProvider
}

// Authenticate implements [Authenticator].
func (a LocalNoOpAuthenticator) Authenticate(ctx context.Context, req sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	_ = req
	var snap OSIdentitySnapshot
	var err error
	if a.OS != nil {
		snap, err = a.OS.Current(ctx)
	}
	if err != nil || a.OS == nil {
		snap = OSIdentitySnapshot{
			PrincipalID:  LocalUnknownOSPrincipalID,
			DisplayName:  "",
			FallbackUsed: true,
		}
	}
	id := strings.TrimSpace(snap.PrincipalID)
	if id == "" {
		id = LocalUnknownOSPrincipalID
		snap.FallbackUsed = true
	}
	return sdkauth.Decision{
		Outcome:        sdkauth.OutcomeAllow,
		Principal:      execview.PrincipalView{ID: id, DisplayName: strings.TrimSpace(snap.DisplayName)},
		SatisfiedLevel: sdkauth.LevelNone,
	}, nil
}
