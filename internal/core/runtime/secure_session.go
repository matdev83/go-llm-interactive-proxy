package runtime

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
)

// SecureSessionRecorder is an alias for the secure-session gate recording port implemented by [app.Recorder].
type SecureSessionRecorder = app.GateRecording

func principalRefFromView(p execview.PrincipalView) domain.PrincipalRef {
	r := domain.PrincipalRef{ID: strings.TrimSpace(p.ID)}
	if len(p.Claims) == 0 {
		return r
	}
	if v := strings.TrimSpace(p.Claims["issuer"]); v != "" {
		r.Issuer = v
	}
	if v := strings.TrimSpace(p.Claims["tenant"]); v != "" {
		r.Tenant = v
	}
	return r
}

func (e *Executor) secureSessionActive() bool {
	return e != nil && e.SecureSessionEnabled && e.SecureSession != nil
}

func (e *Executor) secureSessionForAttempt() *app.Manager {
	if e == nil || !e.secureSessionActive() {
		return nil
	}
	return e.SecureSession
}
