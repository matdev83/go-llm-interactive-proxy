package gemini

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNew_Open_nilContext(t *testing.T) {
	t.Parallel()
	be := New(Config{BaseURL: "https://example.com", APIKey: "k"})
	_, err := be.Open(nil, lipapi.Call{}, routing.AttemptCandidate{}) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
}
