package modelregistry_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

func TestRegistry_LookupByCanonicalIDPreservesBackendOrder(t *testing.T) {
	t.Parallel()

	reg, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID: "openrouter",
			Kind:      "openrouter",
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "openai/gpt-4o", NativeID: "openai/gpt-4o"},
			}},
		},
		{
			BackendID: "openai-direct",
			Kind:      "openai-responses",
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	got, ok := reg.Lookup("openai/gpt-4o")
	if !ok {
		t.Fatal("Lookup(openai/gpt-4o) ok = false")
	}
	if len(got) != 2 {
		t.Fatalf("len(Lookup) = %d, want 2", len(got))
	}
	if got[0].BackendID != "openrouter" || got[1].BackendID != "openai-direct" {
		t.Fatalf("backend order = %q, %q", got[0].BackendID, got[1].BackendID)
	}
	if got[0].NativeID != "openai/gpt-4o" || got[1].NativeID != "gpt-4o" {
		t.Fatalf("native ids = %q, %q", got[0].NativeID, got[1].NativeID)
	}
}

func TestBuildAppliesFetchTimeoutPerBackend(t *testing.T) {
	t.Parallel()

	reg, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:    "first",
			Kind:         "test",
			FetchTimeout: 50 * time.Millisecond,
			Provider: delayedInventoryProvider{
				delay:  25 * time.Millisecond,
				models: []modelinventory.Model{{CanonicalID: "vendor/first", NativeID: "first"}},
			},
		},
		{
			BackendID:    "second",
			Kind:         "test",
			FetchTimeout: 50 * time.Millisecond,
			Provider: delayedInventoryProvider{
				delay:  25 * time.Millisecond,
				models: []modelinventory.Model{{CanonicalID: "vendor/second", NativeID: "second"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := reg.All(); len(got) != 2 {
		t.Fatalf("models len = %d, want 2", len(got))
	}
}

func TestBuildPerBackendFetchTimeoutCancelsSlowBackend(t *testing.T) {
	t.Parallel()

	_, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{{
		BackendID:    "slow",
		Kind:         "test",
		FetchTimeout: 10 * time.Millisecond,
		Provider: delayedInventoryProvider{
			delay:  100 * time.Millisecond,
			models: []modelinventory.Model{{CanonicalID: "vendor/slow", NativeID: "slow"}},
		},
	}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Build() error = %v, want context deadline exceeded", err)
	}
}

func TestRegistry_LookupReturnsDefensiveCopy(t *testing.T) {
	t.Parallel()

	reg, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID: "openai",
			Kind:      "openai-responses",
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	got, ok := reg.Lookup("openai/gpt-4o")
	if !ok {
		t.Fatal("Lookup(openai/gpt-4o) ok = false")
	}
	got[0].BackendID = "mutated"

	got2, ok := reg.Lookup("openai/gpt-4o")
	if !ok {
		t.Fatal("second Lookup(openai/gpt-4o) ok = false")
	}
	if got2[0].BackendID != "openai" {
		t.Fatalf("Lookup returned mutable backing slice: backend = %q", got2[0].BackendID)
	}
}

func TestRegistry_ConcurrentLookup(t *testing.T) {
	t.Parallel()

	reg, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID: "openai",
			Kind:      "openai-responses",
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"},
				{CanonicalID: "openai/gpt-4.1", NativeID: "gpt-4.1"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			for range 1000 {
				got, ok := reg.Lookup("openai/gpt-4o")
				if !ok || len(got) != 1 || got[0].BackendID != "openai" {
					t.Errorf("Lookup() = %+v, %v", got, ok)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestBuildRejectsInvalidInventory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []modelregistry.BackendInventory
		want error
	}{
		{
			name: "nil provider",
			in: []modelregistry.BackendInventory{{
				BackendID: "openai",
				Kind:      "openai-responses",
			}},
			want: modelregistry.ErrMissingProvider,
		},
		{
			name: "model without canonical id",
			in: []modelregistry.BackendInventory{{
				BackendID: "openai",
				Kind:      "openai-responses",
				Provider:  modelinventory.StaticProvider{Models: []modelinventory.Model{{NativeID: "gpt-4o"}}},
			}},
			want: modelregistry.ErrInvalidModel,
		},
		{
			name: "canonical id without vendor",
			in: []modelregistry.BackendInventory{{
				BackendID: "openai",
				Kind:      "openai-responses",
				Provider:  modelinventory.StaticProvider{Models: []modelinventory.Model{{CanonicalID: "gpt-4o", NativeID: "gpt-4o"}}},
			}},
			want: modelregistry.ErrInvalidCanonicalID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := modelregistry.Build(context.Background(), tt.in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Build() error = %v, want %v", err, tt.want)
			}
		})
	}
}

type delayedInventoryProvider struct {
	delay  time.Duration
	models []modelinventory.Model
}

func (p delayedInventoryProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	select {
	case <-ctx.Done():
		return modelinventory.Snapshot{}, ctx.Err()
	case <-time.After(p.delay):
		return modelinventory.Snapshot{
			Source:   modelinventory.SourceRemote,
			LoadedAt: time.Unix(500, 0).UTC(),
			Models:   p.models,
		}, nil
	}
}
