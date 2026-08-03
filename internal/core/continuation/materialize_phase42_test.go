package continuation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	corecont "github.com/matdev83/go-llm-interactive-proxy/internal/core/continuation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

func phase42Item(id string, role lipapi.Role) lipapi.Item {
	return lipapi.Item{ID: id, Kind: lipapi.ItemKindMessage, Role: role, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: id}}}
}

func TestMaterializeExactTrajectoryOrderAndIsolation(t *testing.T) {
	store := corecont.NewMemoryStore()
	scope := lipcont.Scope{PrincipalID: "p", SessionID: "s"}
	ctx := context.Background()
	reserve := func(previous lipcont.ResponseID, input, output []lipapi.Item) lipcont.ResponseID {
		id, err := store.Reserve(ctx, scope, lipcont.StoragePolicy{TTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutTerminal(ctx, lipcont.ContinuationRecord{
			ID: id, Scope: scope, PreviousID: previous, InputItems: input, OutputItems: output,
			Terminal: true, ProfileID: "p", Lineage: lipcont.Lineage{ProfileID: "p"},
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := reserve("", []lipapi.Item{phase42Item("in-1", lipapi.RoleUser)}, []lipapi.Item{phase42Item("out-1", lipapi.RoleAssistant)})
	second := reserve(first, []lipapi.Item{phase42Item("in-2", lipapi.RoleUser)}, []lipapi.Item{phase42Item("out-2", lipapi.RoleAssistant)})
	newInput := []lipapi.Item{phase42Item("in-3", lipapi.RoleUser)}
	got, err := lipcont.Materialize(ctx, lipcont.MaterializeInput{Store: store, Scope: scope, StartID: second, NewInput: newInput, Bounds: lipcont.Bounds{MaxChainDepth: 4, MaxMaterializedItems: 10, MaxMaterializedBytes: 1 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"in-1", "out-1", "in-2", "out-2", "in-3"}
	if len(got.Items) != len(want) {
		t.Fatalf("items=%d want %d", len(got.Items), len(want))
	}
	for i, id := range want {
		if got.Items[i].ID != id {
			t.Fatalf("item[%d]=%q want %q", i, got.Items[i].ID, id)
		}
	}
	got.Items[0].ID = "mutated"
	newInput[0].ID = "mutated-input"
	again, err := lipcont.Materialize(ctx, lipcont.MaterializeInput{Store: store, Scope: scope, StartID: second, NewInput: []lipapi.Item{phase42Item("in-3", lipapi.RoleUser)}, Bounds: lipcont.Bounds{MaxChainDepth: 4, MaxMaterializedItems: 10, MaxMaterializedBytes: 1 << 20}})
	if err != nil || again.Items[0].ID != "in-1" {
		t.Fatalf("materialized state aliased: first=%q err=%v", again.Items[0].ID, err)
	}

	call, _, err := corecont.MaterializeCall(ctx, lipcont.MaterializeInput{Store: store, Scope: scope, StartID: second, NewInput: []lipapi.Item{phase42Item("in-3", lipapi.RoleUser)}, Bounds: lipcont.Bounds{MaxChainDepth: 4, MaxMaterializedItems: 10, MaxMaterializedBytes: 1 << 20}}, lipapi.Call{Session: lipapi.SessionRef{ClientSessionID: "client-only"}})
	if err != nil || call.Session.ClientSessionID != "" || len(call.Items) != 5 {
		t.Fatalf("call materialization leaked client state: call=%+v err=%v", call, err)
	}
}

func TestMaterializeItemBound(t *testing.T) {
	store := corecont.NewMemoryStore()
	scope := lipcont.Scope{PrincipalID: "p", SessionID: "s"}
	id, err := store.Reserve(context.Background(), scope, lipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTerminal(context.Background(), lipcont.ContinuationRecord{ID: id, Scope: scope, Terminal: true, InputItems: []lipapi.Item{phase42Item("in", lipapi.RoleUser)}, OutputItems: []lipapi.Item{phase42Item("out", lipapi.RoleAssistant)}}); err != nil {
		t.Fatal(err)
	}
	_, err = lipcont.Materialize(context.Background(), lipcont.MaterializeInput{Store: store, Scope: scope, StartID: id, NewInput: []lipapi.Item{phase42Item("new", lipapi.RoleUser)}, Bounds: lipcont.Bounds{MaxChainDepth: 2, MaxMaterializedItems: 2}})
	if !errors.Is(err, lipcont.ErrMaterializedItemsExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestMaterializeCarriesNewInputRequirementsAndNativeLineage(t *testing.T) {
	store := corecont.NewMemoryStore()
	scope := lipcont.Scope{PrincipalID: "p", SessionID: "s"}
	id, err := store.Reserve(context.Background(), scope, lipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	record := lipcont.ContinuationRecord{
		ID: id, Scope: scope, Terminal: true,
		InputItems:         []lipapi.Item{phase42Item("old", lipapi.RoleUser)},
		NativeRequirements: []lipcont.NativeRequirement{{BackendID: "provider-a", Model: "model-a", Kind: "reasoning", Dialect: "dialect-a", Implementor: "impl-a"}},
	}
	if err := store.PutTerminal(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	newInput := []lipapi.Item{{Kind: lipapi.ItemKindReasoning, Reasoning: &lipapi.ReasoningItem{Reasoning: &lipapi.ReasoningPart{Dialect: "dialect-b", Text: "history"}}}}
	got, err := lipcont.Materialize(context.Background(), lipcont.MaterializeInput{
		Store: store, Scope: scope, StartID: id, NewInput: newInput,
		Bounds: lipcont.Bounds{MaxChainDepth: 2, MaxMaterializedItems: 8, MaxMaterializedBytes: 1 << 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.NativeRequirements) != 1 || got.NativeRequirements[0].Dialect != "dialect-a" {
		t.Fatalf("native requirements=%+v", got.NativeRequirements)
	}
	if len(got.Requirements.ReasoningDialects) != 1 || got.Requirements.ReasoningDialects[0].Dialect != "dialect-b" {
		t.Fatalf("requirements=%+v", got.Requirements)
	}
}

func TestMaterializeCallPreservesCoreSessionUntilWireSanitization(t *testing.T) {
	store := corecont.NewMemoryStore()
	scope := lipcont.Scope{PrincipalID: "p", SessionID: "s"}
	id, err := store.Reserve(context.Background(), scope, lipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTerminal(context.Background(), lipcont.ContinuationRecord{
		ID: id, Scope: scope, Terminal: true, InputItems: []lipapi.Item{phase42Item("old", lipapi.RoleUser)},
	}); err != nil {
		t.Fatal(err)
	}
	call, _, err := corecont.MaterializeCall(context.Background(), lipcont.MaterializeInput{
		Store: store, Scope: scope, StartID: id, NewInput: []lipapi.Item{phase42Item("new", lipapi.RoleUser)},
		Bounds: lipcont.Bounds{MaxChainDepth: 2, MaxMaterializedItems: 8, MaxMaterializedBytes: 1 << 20},
	}, lipapi.Call{Session: lipapi.SessionRef{
		ClientSessionID: "client", ContinuityKey: "key", ALegID: "a-leg",
		AuthoritativeSessionID: "proxy", ResumeToken: "token",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if call.Session.ALegID != "a-leg" || call.Session.ClientSessionID != "" || call.Session.ContinuityKey != "" || call.Session.AuthoritativeSessionID != "" || call.Session.ResumeToken != "" {
		t.Fatalf("session=%+v", call.Session)
	}
}
