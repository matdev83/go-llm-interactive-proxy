package openairesponses

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNew_rejectsEmptyBaseURL(t *testing.T) {
	t.Parallel()
	be := New(Config{BaseURL: "", APIKey: "k"})
	_, err := be.Open(context.Background(), lipapi.Call{}, routing.AttemptCandidate{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestNew_rejectsWhitespaceOnlyBaseURL(t *testing.T) {
	t.Parallel()
	be := New(Config{BaseURL: " \t", APIKey: "k"})
	_, err := be.Open(context.Background(), lipapi.Call{}, routing.AttemptCandidate{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNew_Open_nilContext(t *testing.T) {
	t.Parallel()
	be := New(Config{BaseURL: "https://example.com/v1", APIKey: "k"})
	_, err := be.Open(nil, lipapi.Call{}, routing.AttemptCandidate{}) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
}
