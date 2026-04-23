package completion_test

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
)

func TestOutcomeValidate(t *testing.T) {
	t.Parallel()
	if err := completion.PassOriginalOutcome().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := completion.ReplayOriginalOutcome().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := completion.ReplaceOutcome([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := completion.RejectOutcome(errors.New("no")).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (completion.Outcome{Kind: completion.OutcomePassOriginal, Events: []lipapi.Event{{}}}).Validate(); err == nil {
		t.Fatal("expected err")
	}
	if err := (completion.Outcome{Kind: completion.OutcomeReject}).Validate(); err == nil {
		t.Fatal("expected err")
	}
}

func TestLastEventKind(t *testing.T) {
	t.Parallel()
	if completion.LastEventKind(nil) != "" {
		t.Fatal("empty")
	}
	if completion.LastEventKind([]lipapi.Event{{Kind: lipapi.EventTextDelta}}) != lipapi.EventTextDelta {
		t.Fatal("delta")
	}
}
