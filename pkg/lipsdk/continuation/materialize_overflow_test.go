package continuation_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

func TestMaterializeNearMaxIntLimits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scope := lipcont.Scope{TenantID: "tenant", PrincipalID: "principal", SessionID: "session"}
	store := lipcont.NewMemoryStore()

	id, err := store.Reserve(ctx, scope, lipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTerminal(ctx, lipcont.ContinuationRecord{
		ID: id, Scope: scope, Terminal: true,
		InputItems:  []lipapi.Item{overflowItem("in-1", lipapi.RoleUser)},
		OutputItems: []lipapi.Item{overflowItem("out-1", lipapi.RoleAssistant)},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := lipcont.Materialize(ctx, lipcont.MaterializeInput{
		Store: store, Scope: scope, StartID: id,
		NewInput: []lipapi.Item{overflowItem("in-2", lipapi.RoleUser)},
		Bounds: lipcont.Bounds{
			MaxChainDepth:        2,
			MaxMaterializedItems: math.MaxInt,
			MaxMaterializedBytes: math.MaxInt64,
		},
	})
	if err != nil {
		t.Fatalf("materialize with near-MaxInt limits: %v", err)
	}
	want := []string{"in-1", "out-1", "in-2"}
	if len(got.Items) != len(want) {
		t.Fatalf("items=%d want %d", len(got.Items), len(want))
	}
	for i, id := range want {
		if got.Items[i].ID != id {
			t.Fatalf("item[%d]=%q want %q", i, got.Items[i].ID, id)
		}
	}
	if got.ChainDepth != 1 || got.TotalBytes <= 0 {
		t.Fatalf("trajectory=%+v", got)
	}
}

func overflowItem(id string, role lipapi.Role) lipapi.Item {
	return lipapi.Item{ID: id, Kind: lipapi.ItemKindMessage, Role: role, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: id}}}
}
