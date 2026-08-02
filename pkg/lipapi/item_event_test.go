package lipapi_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func compactionItemEvent(id, encrypted string) lipapi.Event {
	return lipapi.Event{
		Kind: lipapi.EventItem,
		Item: &lipapi.Item{
			Kind:   lipapi.ItemKindCompaction,
			ID:     id,
			Status: lipapi.ItemStatusCompleted,
			Compaction: &lipapi.CompactionItem{
				EncryptedContent: encrypted,
			},
		},
	}
}

func TestValidateEventEnvelope_itemEventRequiresCarriedItem(t *testing.T) {
	t.Parallel()
	ev := &lipapi.Event{Kind: lipapi.EventItem}
	if err := lipapi.ValidateEventEnvelope(ev); err == nil {
		t.Fatal("expected error for item event without carried item")
	}
}

func TestValidateEventEnvelope_itemEventValidatesCarriedItem(t *testing.T) {
	t.Parallel()
	ev := compactionItemEvent("cmp_1", "enc:cmp_1")
	if err := lipapi.ValidateEventEnvelope(&ev); err != nil {
		t.Fatalf("valid item event rejected: %v", err)
	}
}

func TestValidateEventEnvelope_itemOnlyAllowedOnItemEvent(t *testing.T) {
	t.Parallel()
	ev := compactionItemEvent("cmp_1", "enc")
	ev.Kind = lipapi.EventResponseStarted
	if err := lipapi.ValidateEventEnvelope(&ev); err == nil {
		t.Fatal("expected error for carried item on non-item event")
	}
}

func TestValidateEventEnvelope_itemEventRejectsInvalidCarriedItem(t *testing.T) {
	t.Parallel()
	ev := lipapi.Event{
		Kind: lipapi.EventItem,
		Item: &lipapi.Item{
			Kind: lipapi.ItemKindCompaction,
			ID:   "cmp_bad",
			Compaction: &lipapi.CompactionItem{
				EncryptedContent: string(make([]byte, lipapi.MaxCompactionEncryptedContentBytes+1)),
			},
		},
	}
	if err := lipapi.ValidateEventEnvelope(&ev); err == nil {
		t.Fatal("expected error for oversized compaction encrypted_content")
	}
}

func TestValidateEventSequence_allowsItemEventAfterStart(t *testing.T) {
	t.Parallel()
	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		compactionItemEvent("cmp_1", "enc"),
		{Kind: lipapi.EventResponseFinished},
	}
	if err := lipapi.ValidateEventSequence(events); err != nil {
		t.Fatalf("sequence rejected: %v", err)
	}
}

func TestValidateEventSequence_itemEventBeforeStartRejected(t *testing.T) {
	t.Parallel()
	events := []lipapi.Event{
		compactionItemEvent("cmp_1", "enc"),
	}
	if err := lipapi.ValidateEventSequence(events); err == nil {
		t.Fatal("expected error for item event before response started")
	}
}

func TestCompactionItem_RoundTripsEncryptedContent(t *testing.T) {
	t.Parallel()
	item := lipapi.Item{
		Kind:   lipapi.ItemKindCompaction,
		ID:     "cmp_rt",
		Status: lipapi.ItemStatusCompleted,
		Compaction: &lipapi.CompactionItem{
			EncapsulatedID:   "enc_1",
			Dialect:          "openresponses.2026-04-24",
			Implementor:      "provider-x",
			EncryptedContent: "gAAAAABpayload",
		},
	}
	call := lipapi.Call{Items: []lipapi.Item{item}}
	if err := call.Validate(); err != nil {
		t.Fatalf("call with compaction item invalid: %v", err)
	}
}
