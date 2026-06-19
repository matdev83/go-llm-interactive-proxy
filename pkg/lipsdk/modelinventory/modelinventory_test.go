package modelinventory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

func TestStaticProvider_LoadModels(t *testing.T) {
	t.Parallel()

	want := []modelinventory.Model{
		{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"},
		{CanonicalID: "anthropic/claude-sonnet", NativeID: "claude-sonnet"},
	}
	p := modelinventory.StaticProvider{Source: modelinventory.SourceStaticInline, Models: want}

	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels() error = %v", err)
	}
	if snap.Source != modelinventory.SourceStaticInline {
		t.Fatalf("Source = %q", snap.Source)
	}
	if len(snap.Models) != len(want) {
		t.Fatalf("models len = %d, want %d", len(snap.Models), len(want))
	}
	snap.Models[0].CanonicalID = "mutated/model"

	snap2, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels() second error = %v", err)
	}
	if snap2.Models[0].CanonicalID != want[0].CanonicalID {
		t.Fatalf("provider returned mutable backing slice: got %q", snap2.Models[0].CanonicalID)
	}
}

func TestStaticProvider_LoadModelsNilContext(t *testing.T) {
	t.Parallel()

	//nolint:staticcheck // defensive nil-context handling is part of the provider contract
	_, err := (modelinventory.StaticProvider{}).LoadModels(nil)
	if !errors.Is(err, modelinventory.ErrNilContext) {
		t.Fatalf("LoadModels(nil) error = %v, want ErrNilContext", err)
	}
}

func TestStaticProvider_LoadModelsReportsLoadTimeWhenLoadedAtUnset(t *testing.T) {
	t.Parallel()

	before := time.Now()
	snap, err := modelinventory.StaticProvider{
		Models: []modelinventory.Model{{CanonicalID: "vendor/model", NativeID: "model"}},
	}.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	if snap.LoadedAt.Before(before) || snap.LoadedAt.After(after) {
		t.Fatalf("LoadedAt = %s, want between %s and %s", snap.LoadedAt, before, after)
	}
}

func TestStaticProvider_ImplementsStaticInventoryMarker(t *testing.T) {
	t.Parallel()

	var _ modelinventory.StaticInventory = modelinventory.StaticProvider{}
}

func TestErrorProvider_ImplementsStaticInventoryMarker(t *testing.T) {
	t.Parallel()

	var _ modelinventory.StaticInventory = modelinventory.ErrorProvider{}
	if !(modelinventory.ErrorProvider{}).StaticInventory() {
		t.Fatal("ErrorProvider.StaticInventory() = false, want true")
	}
}
