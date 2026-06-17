package openrouter_test

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openrouter"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNew_openReturnsConfigErrorForEmptyBaseURL(t *testing.T) {
	t.Parallel()
	be := openrouter.New(openrouter.Config{APIKey: "sk-test"})
	_, err := be.Open(nil, lipapi.Call{}, routing.AttemptCandidate{})
	if err == nil {
		t.Fatal("expected config error")
	}
}

func TestNew_openRejectsNilContext(t *testing.T) {
	t.Parallel()
	be := openrouter.New(openrouter.Config{BaseURL: "http://127.0.0.1", APIKey: "sk-test"})
	_, err := be.Open(nil, lipapi.Call{}, routing.AttemptCandidate{})
	if err == nil {
		t.Fatal("expected nil context error")
	}
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
}
