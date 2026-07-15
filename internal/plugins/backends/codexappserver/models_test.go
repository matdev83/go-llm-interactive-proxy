package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/codexcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type recordingStarter struct {
	mu   sync.Mutex
	n    int
	last []string
}

func (s *recordingStarter) Start(cmd []string, _ string, _ []string) (acp.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	s.last = append([]string(nil), cmd...)
	return nil, errors.New("recording starter: refuse start")
}

func (s *recordingStarter) starts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// acceptLoadedInventory loads from inv then commits via AcceptInventory, matching
// core's publish path (LoadModels alone does not update the Open allowlist).
func acceptLoadedInventory(t *testing.T, inv modelinventory.Provider) {
	t.Helper()
	if inv == nil {
		t.Fatal("nil inventory")
	}
	snap, err := inv.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a, ok := inv.(modelinventory.AcceptedInventory)
	if !ok {
		t.Fatalf("inventory type %T missing AcceptedInventory", inv)
	}
	a.AcceptInventory(snap.Models)
}

func openCall(t *testing.T, selector string) lipapi.Call {
	t.Helper()
	ws := t.TempDir()
	cwd, err := json.Marshal(ws)
	if err != nil {
		t.Fatal(err)
	}
	return lipapi.Call{
		Route: lipapi.RouteIntent{Selector: selector},
		Extensions: map[string]json.RawMessage{
			"acp.cwd": cwd,
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
		}},
	}
}

func TestOpen_fallbackCatalogSlugRejectsWithoutStartingProcess(t *testing.T) {
	t.Parallel()

	cat, err := codexcatalog.LoadFallback("")
	if err != nil {
		t.Fatal(err)
	}
	slugs := cat.RoutableSlugs()
	if len(slugs) == 0 {
		t.Fatal("expected shipped catalog slugs")
	}

	starter := &recordingStarter{}
	be := NewWithStarter(Config{
		ConnectorConfig:    acp.ConnectorConfig{DefaultWorkspace: t.TempDir()},
		ModelCatalog:       cat,
		ModelCatalogSource: codexcatalog.SourceShippedFallback,
	}, starter)
	acceptLoadedInventory(t, be.ModelInventory)

	call := openCall(t, ID+":"+slugs[0])
	_, err = be.Open(context.Background(), call, routing.AttemptCandidate{})
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("Open fallback slug error = %v, want ErrUnknownModel", err)
	}
	if starter.starts() != 0 {
		t.Fatalf("process starts = %d, want 0", starter.starts())
	}
}

func TestOpen_configuredOverrideExcludesSlugWithoutStartingProcess(t *testing.T) {
	t.Parallel()

	starter := &recordingStarter{}
	be := NewWithStarter(Config{
		ConnectorConfig: acp.ConnectorConfig{DefaultWorkspace: t.TempDir()},
		Inventory: modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticBuiltin,
			Models: []modelinventory.Model{
				{CanonicalID: "openai/auto", NativeID: "auto"},
				{CanonicalID: "openai/gpt-5.4", NativeID: "gpt-5.4"},
				{CanonicalID: "openai/gpt-5.3-codex", NativeID: "gpt-5.3-codex"},
			},
		},
	}, starter)
	acceptLoadedInventory(t, be.ModelInventory)

	ti, ok := be.ModelInventory.(*acp.TrackingInventory)
	if !ok {
		t.Fatalf("ModelInventory type = %T, want *acp.TrackingInventory", be.ModelInventory)
	}
	ti.SetInner(modelinventory.StaticProvider{
		Source: modelinventory.SourceStaticInline,
		Models: []modelinventory.Model{
			{CanonicalID: "openai/auto", NativeID: "auto"},
			{CanonicalID: "openai/gpt-5.4", NativeID: "gpt-5.4"},
		},
	})
	acceptLoadedInventory(t, be.ModelInventory)

	call := openCall(t, ID+":gpt-5.3-codex")
	_, err := be.Open(context.Background(), call, routing.AttemptCandidate{})
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("Open excluded slug error = %v, want ErrUnknownModel", err)
	}
	if starter.starts() != 0 {
		t.Fatalf("process starts = %d, want 0", starter.starts())
	}
}

func TestOpen_configuredOverrideAllowsNativeBeforeStart(t *testing.T) {
	t.Parallel()

	starter := &recordingStarter{}
	be := NewWithStarter(Config{
		ConnectorConfig: acp.ConnectorConfig{DefaultWorkspace: t.TempDir()},
		Inventory: modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticInline,
			Models: []modelinventory.Model{
				{CanonicalID: "openai/auto", NativeID: "auto"},
				{CanonicalID: "openai/gpt-5.4", NativeID: "gpt-5.4"},
			},
		},
	}, starter)
	acceptLoadedInventory(t, be.ModelInventory)

	call := openCall(t, ID+":gpt-5.4")
	_, err := be.Open(context.Background(), call, routing.AttemptCandidate{})
	// Starter refuses start after allowlist passes — proves gate allowed native.
	if err == nil {
		t.Fatal("Open expected starter refusal after allowlist pass")
	}
	if errors.Is(err, ErrUnknownModel) {
		t.Fatalf("Open must allow configured native, got ErrUnknownModel: %v", err)
	}
	if starter.starts() != 1 {
		t.Fatalf("process starts = %d, want 1 (allowlist passed)", starter.starts())
	}
}

func TestOpen_autoAlwaysAllowedWithoutInventoryRows(t *testing.T) {
	t.Parallel()

	starter := &recordingStarter{}
	be := NewWithStarter(Config{
		ConnectorConfig: acp.ConnectorConfig{DefaultWorkspace: t.TempDir()},
		Inventory: modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticInline,
			Models: nil,
		},
	}, starter)
	acceptLoadedInventory(t, be.ModelInventory)

	call := openCall(t, ID+":auto")
	_, err := be.Open(context.Background(), call, routing.AttemptCandidate{})
	if errors.Is(err, ErrUnknownModel) {
		t.Fatalf("auto must remain allowed: %v", err)
	}
	if starter.starts() != 1 {
		t.Fatalf("process starts = %d, want 1", starter.starts())
	}
}

func TestOpen_unknownRejectsBeforeLoadModels(t *testing.T) {
	t.Parallel()

	starter := &recordingStarter{}
	be := NewWithStarter(Config{
		ConnectorConfig: acp.ConnectorConfig{DefaultWorkspace: t.TempDir()},
	}, starter)

	call := openCall(t, ID+":gpt-5.4")
	_, err := be.Open(context.Background(), call, routing.AttemptCandidate{})
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("Open without LoadModels error = %v, want ErrUnknownModel", err)
	}
	if starter.starts() != 0 {
		t.Fatalf("process starts = %d, want 0", starter.starts())
	}
}

func TestResolveModel_mapsCanonicalFriendlyToNative(t *testing.T) {
	t.Parallel()

	idx := acp.NewModelIndex(nil)
	idx.Replace([]modelinventory.Model{
		{CanonicalID: "openai/auto", NativeID: "auto"},
		{CanonicalID: "openai/friendly", NativeID: "actual-native"},
	})
	spec := &codexSpec{cfg: Config{}, index: idx}
	p := &codexProtocol{spec: spec}

	call := &lipapi.Call{Route: lipapi.RouteIntent{Selector: ID + ":openai/friendly"}}
	if got := p.ResolveModel(call); got != "actual-native" {
		t.Fatalf("ResolveModel = %q, want actual-native", got)
	}

	unknown := &lipapi.Call{Route: lipapi.RouteIntent{Selector: ID + ":openai/missing"}}
	if got := p.ResolveModel(unknown); got != "" {
		t.Fatalf("unknown ResolveModel = %q, want empty", got)
	}
}

func TestResolveAllowedModel_nilSpec(t *testing.T) {
	t.Parallel()

	var spec *codexSpec
	if _, err := spec.resolveAllowedModel(&lipapi.Call{}); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("nil spec error = %v, want ErrUnknownModel", err)
	}
}

func TestNew_usesTrackingInventory(t *testing.T) {
	t.Parallel()

	be, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := be.ModelInventory.(*acp.TrackingInventory); !ok {
		t.Fatalf("ModelInventory type = %T, want *acp.TrackingInventory", be.ModelInventory)
	}
}
