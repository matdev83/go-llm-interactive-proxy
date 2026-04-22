package acp

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNew_Open_nilContext(t *testing.T) {
	t.Parallel()
	be := New(Config{BaseURL: "http://127.0.0.1:9"})
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, err := be.Open(nil, call, routing.AttemptCandidate{}) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
}
