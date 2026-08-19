package extensions_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
)

type lifecyclePreserver struct {
	preservingCallback
	failed func() error
	after  func(lipapi.Event) error
}

func (p lifecyclePreserver) RequestOpenFailed(context.Context, compaction.PreservationMeta, compaction.Services) error {
	if p.failed != nil {
		return p.failed()
	}
	return nil
}

func (p lifecyclePreserver) AfterResponseRelease(_ context.Context, ev lipapi.Event, _ compaction.PreservationMeta, _ compaction.Services) error {
	if p.after != nil {
		return p.after(ev)
	}
	return nil
}

func TestRunCompactionPreserverRequestOpenFailed_optionalOrderedFailOpen(t *testing.T) {
	t.Parallel()
	order := make([]string, 0, 3)
	ps := []compaction.Preserver{
		lifecyclePreserver{preservingCallback: preservingCallback{id: "one"}, failed: func() error {
			order = append(order, "one")
			return errors.New("ignored")
		}},
		preservingCallback{id: "legacy"},
		lifecyclePreserver{preservingCallback: preservingCallback{id: "panic"}, failed: func() error {
			order = append(order, "panic")
			panic("ignored")
		}},
	}
	if err := extensions.RunCompactionPreserverRequestOpenFailed(context.Background(), nil, nil, ps, compaction.PreservationMeta{TraceID: "t"}, compaction.Services{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"one", "panic"}) {
		t.Fatalf("order=%v", order)
	}
}

func TestRunCompactionPreserverAfterResponseRelease_isolatedFailOpen(t *testing.T) {
	t.Parallel()
	original := lipapi.Event{Kind: lipapi.EventItem, Item: &lipapi.Item{Kind: lipapi.ItemKindMessage, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "original"}}}}
	seen := make([]string, 0, 3)
	ps := []compaction.Preserver{
		lifecyclePreserver{preservingCallback: preservingCallback{id: "error"}, after: func(ev lipapi.Event) error {
			seen = append(seen, ev.Item.Content[0].Text)
			ev.Item.Content[0].Text = "changed"
			return errors.New("ignored")
		}},
		lifecyclePreserver{preservingCallback: preservingCallback{id: "panic"}, after: func(ev lipapi.Event) error {
			seen = append(seen, ev.Item.Content[0].Text)
			panic("ignored")
		}},
		lifecyclePreserver{preservingCallback: preservingCallback{id: "ok"}, after: func(ev lipapi.Event) error {
			seen = append(seen, ev.Item.Content[0].Text)
			return nil
		}},
	}
	if err := extensions.RunCompactionPreserverAfterResponseRelease(context.Background(), nil, nil, ps, original, compaction.PreservationMeta{}, compaction.Services{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, []string{"original", "original", "original"}) {
		t.Fatalf("callback isolation=%v", seen)
	}
	if got := original.Item.Content[0].Text; got != "original" {
		t.Fatalf("after-release callback mutated released event: %q", got)
	}
}
