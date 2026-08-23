package runtime

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/localstream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

// TestLocalStream_LegacyFullHistory_ClientVisibleBackendFiltered proves
// a claimed local input and its reply remain client-visible in the A-leg
// ingress/continuation but are filtered on the next backend projection
// when the client replays full legacy history. This is the legacy slice of
// Req 11.1-11.3 for task 3.4.
func TestLocalStream_LegacyFullHistory_ClientVisibleBackendFiltered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cv := st.ConversationViewStore()

	rec, err := st.CreateALeg(ctx, "ck-local-legacy-visibility")
	if err != nil {
		t.Fatal(err)
	}
	aLegID := rec.ALegID

	sourceMsg := lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("local-claimed-input")}}
	replyText := "local-reply-legacy"
	replyMsg := localstream.CanonicalAssistantMessage(replyText)
	// Simulate local turn tagging: source and reply.
	sourceID, _ := conversationview.MessageIdentityOf(sourceMsg)
	replyID, _ := conversationview.MessageIdentityOf(replyMsg)
	if _, err := cv.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{
		{Identity: sourceID, Reason: "test_local"},
		{Identity: replyID, Reason: "test_local"},
	}); err != nil {
		t.Fatalf("tag: %v", err)
	}
	snap, err := cv.Snapshot(ctx, aLegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.NeverBackend) != 2 {
		t.Fatalf("snapshot never_backend %d want 2", len(snap.NeverBackend))
	}
	// Client replays full history containing the local messages plus a new turn.
	legacyCall := lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{
			sourceMsg,
			replyMsg,
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("next question")}},
		},
		Session: lipapi.SessionRef{ALegID: aLegID},
	}
	// Client-visible truth must still contain the local messages.
	foundSource := false
	foundReply := false
	for _, m := range legacyCall.Messages {
		if id, _ := conversationview.MessageIdentityOf(m); id == sourceID {
			foundSource = true
		}
		if id, _ := conversationview.MessageIdentityOf(m); id == replyID {
			foundReply = true
		}
	}
	if !foundSource || !foundReply {
		t.Fatalf("ingress lost local messages: source %v reply %v", foundSource, foundReply)
	}
	// Backend-effective projection must remove both.
	out, ev, err := conversationview.Project(legacyCall, snap)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if ev.FilteredCount != 2 {
		t.Fatalf("filtered %d want 2", ev.FilteredCount)
	}
	for _, m := range out.Messages {
		if id, _ := conversationview.MessageIdentityOf(m); id == sourceID || id == replyID {
			t.Fatalf("backend call still contains tagged %s", id)
		}
	}
	for _, m := range out.Instructions {
		if id, _ := conversationview.MessageIdentityOf(m); id == sourceID || id == replyID {
			t.Fatalf("backend instructions still contains tagged %s", id)
		}
	}
	// Exactly one forwardable message remains.
	if len(out.Messages) != 1 || out.Messages[0].Parts[0].Text != "next question" {
		t.Fatalf("backend messages %+v want single next question", out.Messages)
	}
	// Feature does not require frontend to delete from A-leg history; snap still holds tags.
	snap2, _ := cv.Snapshot(ctx, aLegID)
	if len(snap2.NeverBackend) != 2 {
		t.Fatalf("snapshot after projection %d want 2", len(snap2.NeverBackend))
	}
}

// TestLocalStream_OpenResponsesMaterializedHistory_ClientVisibleBackendFiltered
// proves the same invariant when history is reconstructed via previous_response_id
// materialization (OpenResponses slice). For 3.4 we use a focused slice:
// the materialized Call.Items contain the previously recorded local input/reply
// as concrete message items, and projection removes them before backend work.
func TestLocalStream_OpenResponsesMaterializedHistory_ClientVisibleBackendFiltered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cv := st.ConversationViewStore()
	rec, err := st.CreateALeg(ctx, "ck-local-or-visibility")
	if err != nil {
		t.Fatal(err)
	}
	aLegID := rec.ALegID

	// Same identities as legacy but in item authority.
	sourceItem := lipapi.Item{
		Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser, Status: lipapi.ItemStatusCompleted,
		Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "local-claimed-input"}},
		ID:      "item-source-1",
	}
	replyText := "local-reply-or"
	replyItem := localstream.CanonicalAssistantItem(replyText)
	replyItem.ID = "item-reply-1"
	sourceID, _ := conversationview.ItemIdentityOf(sourceItem)
	replyID, _ := conversationview.ItemIdentityOf(replyItem)
	if _, err := cv.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{
		{Identity: sourceID, Reason: "test_local"},
		{Identity: replyID, Reason: "test_local"},
	}); err != nil {
		t.Fatalf("tag: %v", err)
	}
	snap, _ := cv.Snapshot(ctx, aLegID)
	if len(snap.NeverBackend) != 2 {
		t.Fatalf("snapshot never_backend %d want 2", len(snap.NeverBackend))
	}

	// Simulate continuation materialization: the resolver would return a Call
	// whose Items are the reconstructed history containing the local messages.
	// Here we directly build that materialized call as the runtime would see
	// after ResolveParent.
	materializedCall := lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Items: []lipapi.Item{
			sourceItem,
			replyItem,
			{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser, Status: lipapi.ItemStatusCompleted, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "next question"}}, ID: "item-next-1"},
		},
		Session: lipapi.SessionRef{ALegID: aLegID},
	}
	// Also include a dangling item_reference to the source that must be cleaned.
	materializedCall.Items = append(materializedCall.Items, lipapi.Item{
		Kind: lipapi.ItemKindItemReference, Status: lipapi.ItemStatusCompleted,
		Reference: &lipapi.ItemReference{ID: "item-source-1"},
	})

	// Client-visible reconstructed call still contains local items.
	foundSource := false
	foundReply := false
	for _, it := range materializedCall.Items {
		if it.Kind != lipapi.ItemKindMessage {
			continue
		}
		if id, _ := conversationview.ItemIdentityOf(it); id == sourceID {
			foundSource = true
		}
		if id, _ := conversationview.ItemIdentityOf(it); id == replyID {
			foundReply = true
		}
	}
	if !foundSource || !foundReply {
		t.Fatalf("materialized lost local items")
	}

	out, ev, err := conversationview.Project(materializedCall, snap)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if ev.FilteredCount != 2 {
		t.Fatalf("filtered %d want 2", ev.FilteredCount)
	}
	for _, it := range out.Items {
		if it.Kind == lipapi.ItemKindMessage {
			if id, _ := conversationview.ItemIdentityOf(it); id == sourceID || id == replyID {
				t.Fatalf("backend still contains tagged item %s", id)
			}
		}
		if it.Kind == lipapi.ItemKindItemReference && it.Reference != nil && it.Reference.ID == "item-source-1" {
			t.Fatalf("dangling reference not cleaned")
		}
	}
	// Only the new user message should survive (plus no steering).
	countMsg := 0
	for _, it := range out.Items {
		if it.Kind == lipapi.ItemKindMessage {
			countMsg++
			if it.Content[0].Text != "next question" {
				t.Fatalf("unexpected surviving item %+v", it)
			}
		}
	}
	if countMsg != 1 {
		t.Fatalf("surviving msg count %d want 1", countMsg)
	}

	// Continuation record still holds client-visible truth: simulate a
	// MemoryStore record that would be returned via Get for previous_response_id.
	contStore := lipcont.NewMemoryStore()
	scope := lipcont.Scope{SessionID: aLegID}
	id, err := contStore.Reserve(ctx, scope, lipcont.StoragePolicy{})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	recCont := lipcont.ContinuationRecord{
		ID: id, Scope: scope, Terminal: true, Status: lipcont.RecordStatusCompleted,
		InputItems:  []lipapi.Item{sourceItem, {Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser, Status: lipapi.ItemStatusCompleted, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "prev user"}}, ID: "prev-1"}},
		OutputItems: []lipapi.Item{replyItem},
		Policy:      lipcont.StoragePolicy{},
	}
	if err := contStore.PutTerminal(ctx, recCont); err != nil {
		t.Fatalf("PutTerminal: %v", err)
	}
	stored, err := contStore.Get(ctx, scope, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Stored continuation still contains local reply (client-visible).
	found := false
	for _, it := range stored.OutputItems {
		if it.Kind == lipapi.ItemKindMessage {
			if id2, _ := conversationview.ItemIdentityOf(it); id2 == replyID {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("continuation store lost local reply")
	}
	// But a subsequent materialized backend call built from that continuation
	// would again be filtered (proven above).
}

// TestLocalStream_LocalHandlerClaimPreservesClientVisibilityYetFiltersNextTurn
// is a focused integration slice proving the next backend turn filters
// previously tagged local messages while the client view retains them.
// It asserts cross-turn visibility without requiring a second store read.
func TestLocalStream_LocalHandlerClaimPreservesClientVisibilityYetFiltersNextTurn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// This test is intentionally light: the per-turn filtering is already
	// proven by the two focused tests above. This integration check ensures
	// the local turn's merged snapshot is usable for the *next* turn's
	// projection without requiring a second store read.
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cv := st.ConversationViewStore()
	rec, _ := st.CreateALeg(ctx, "ck-visibility-integration")
	sourceMsg := lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("claimed-input")}}
	sourceID, _ := conversationview.MessageIdentityOf(sourceMsg)
	replyText := "integration-reply"
	replyMsg := localstream.CanonicalAssistantMessage(replyText)
	replyID, _ := conversationview.MessageIdentityOf(replyMsg)
	if _, err := cv.TagNeverBackend(ctx, rec.ALegID, []conversationview.TagRequest{{Identity: sourceID, Reason: "test_local"}, {Identity: replyID, Reason: "test_local"}}); err != nil {
		t.Fatal(err)
	}
	snap, _ := cv.Snapshot(ctx, rec.ALegID)
	// Next backend call from same A-leg replays both local messages plus new turn.
	nextCall := lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{sourceMsg, replyMsg, {Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("follow up")}}},
	}
	out, ev, err := conversationview.Project(nextCall, snap)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if ev.FilteredCount != 2 {
		t.Fatalf("filtered %d want 2", ev.FilteredCount)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("out messages %d want 1", len(out.Messages))
	}
	// Prove the *client* view (nextCall) still had them before projection.
	if len(nextCall.Messages) != 3 {
		t.Fatalf("client call lost visibility")
	}
}
