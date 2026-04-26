package testkit

import (
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNewStubExecutorWithSecureSession_smoke(t *testing.T) {
	t.Parallel()
	var cap sync.Map
	ex := NewStubExecutorWithSecureSession(t, SecureSessionStubExecutorOptions{}, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), &cap)
	if ex == nil || ex.SecureSession == nil {
		t.Fatal("expected secure-session wiring")
	}
	if ex.SessionDenialMapper == nil {
		t.Fatal("expected denial mapper")
	}
}
