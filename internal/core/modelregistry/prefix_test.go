package modelregistry_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

func TestBuild_allowsDuplicateBackendPrefixForSameKind(t *testing.T) {
	t.Parallel()

	reg, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "openai-primary",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"},
			}},
		},
		{
			BackendID:       "openai-fallback",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "openai/gpt-4.1", NativeID: "gpt-4.1"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := reg.All(); len(got) != 2 {
		t.Fatalf("models len = %d, want 2", len(got))
	}
}

func TestBuild_rejectsDuplicateBackendPrefixBeforeLoadModels(t *testing.T) {
	t.Parallel()

	first := &prefixCountingProvider{err: errors.New("first must not load")}
	second := &prefixCountingProvider{err: errors.New("second must not load")}
	_, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "backend-a",
			Kind:            "test-a",
			BackendPrefixes: []string{"shared"},
			Provider:        first,
		},
		{
			BackendID:       "backend-b",
			Kind:            "test-b",
			BackendPrefixes: []string{"shared"},
			Provider:        second,
		},
	})
	if !errors.Is(err, modelregistry.ErrDuplicateBackendPrefix) {
		t.Fatalf("Build() error = %v, want ErrDuplicateBackendPrefix", err)
	}
	if !strings.Contains(err.Error(), "backend-a") || !strings.Contains(err.Error(), "backend-b") {
		t.Fatalf("duplicate prefix error = %v, want both backend ids", err)
	}
	if !strings.Contains(err.Error(), "test-a") || !strings.Contains(err.Error(), "test-b") {
		t.Fatalf("duplicate prefix error = %v, want both backend kinds", err)
	}
	if first.calls != 0 || second.calls != 0 {
		t.Fatalf("LoadModels calls = %d/%d, want 0/0", first.calls, second.calls)
	}
}

func TestBuild_rejectsMissingBackendPrefix(t *testing.T) {
	t.Parallel()

	provider := &prefixCountingProvider{models: []modelinventory.Model{{
		CanonicalID: "vendor/model",
		NativeID:    "model",
	}}}
	_, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{{
		BackendID: "openai",
		Kind:      "openai-responses",
		Provider:  provider,
	}})
	if !errors.Is(err, modelregistry.ErrMissingBackendPrefix) {
		t.Fatalf("Build() error = %v, want ErrMissingBackendPrefix", err)
	}
	if provider.calls != 0 {
		t.Fatalf("LoadModels calls = %d, want 0", provider.calls)
	}
}

func TestBuild_rejectsQualifiedCanonicalIDWithRegisteredPrefix(t *testing.T) {
	t.Parallel()

	_, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{{
		BackendID:       "ollama-local",
		Kind:            "ollama",
		BackendPrefixes: []string{"ollama"},
		Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
			{CanonicalID: "ollama:google/gemma4", NativeID: "google/gemma4"},
		}},
	}})
	if !errors.Is(err, modelregistry.ErrInvalidCanonicalID) {
		t.Fatalf("Build() error = %v, want ErrInvalidCanonicalID", err)
	}
}

func TestBuild_allowsSlashCanonicalWhenVendorMatchesRegisteredPrefix(t *testing.T) {
	t.Parallel()

	reg, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{{
		BackendID:       "ollama-local",
		Kind:            "ollama",
		BackendPrefixes: []string{"ollama"},
		Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
			{CanonicalID: "ollama/llama3", NativeID: "llama3:latest"},
		}},
	}})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	got, ok := reg.Lookup("ollama/llama3")
	if !ok || len(got) != 1 || got[0].NativeID != "llama3:latest" {
		t.Fatalf("Lookup(ollama/llama3) = %+v, %v", got, ok)
	}
}

type prefixCountingProvider struct {
	mu     sync.Mutex
	calls  int
	err    error
	models []modelinventory.Model
}

func (p *prefixCountingProvider) LoadModels(context.Context) (modelinventory.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.err != nil {
		return modelinventory.Snapshot{}, p.err
	}
	return modelinventory.Snapshot{
		Source: modelinventory.SourceRemote,
		Models: append([]modelinventory.Model(nil), p.models...),
	}, nil
}
