package huggingface

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
	be := New(Config{BaseURL: "", APIKey: "hf-test"})
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
	be := New(Config{BaseURL: " \t", APIKey: "hf-test"})
	_, err := be.Open(context.Background(), lipapi.Call{}, routing.AttemptCandidate{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestNew_Open_nilContext(t *testing.T) {
	t.Parallel()
	be := New(Config{BaseURL: DefaultBaseURL, APIKey: "hf-test"})
	_, err := be.Open(nil, lipapi.Call{}, routing.AttemptCandidate{}) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
}

func TestNew_Open_noCredentials(t *testing.T) {
	t.Parallel()
	be := New(Config{BaseURL: DefaultBaseURL, APIKey: ""})
	_, err := be.Open(context.Background(), lipapi.Call{}, routing.AttemptCandidate{})
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
}
