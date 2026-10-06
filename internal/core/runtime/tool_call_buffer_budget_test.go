package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

func TestToolCallAssembler_BoundsConcurrentMandatoryBuffers(t *testing.T) {
	fin := newScopedFin(t, "budget", 0, toolcall.BufferingSpec{MaxArgsBytes: toolcall.MaxMandatoryMaxArgsBytes, Overflow: toolcall.OverflowReject}, mandatoryToolName)
	a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, mandatoryCatalog())
	ctx := context.Background()
	delta := strings.Repeat("x", toolcall.MaxMandatoryMaxArgsBytes)
	for i := range 3 {
		id := fmt.Sprintf("call-%d", i)
		_, err := a.ingest(ctx, lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: id, ToolName: mandatoryToolName}, toolcall.Meta{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.ingest(ctx, lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: id, Delta: delta}, toolcall.Meta{})
		if i < 2 && err != nil {
			t.Fatal(err)
		}
		if i == 2 && !errors.Is(err, ErrToolCallBufferBudget) {
			t.Fatal("aggregate argument budget was not enforced")
		}
	}
}

func TestToolCallAssembler_BoundsActiveCallsAndEmptyFragments(t *testing.T) {
	for _, schedule := range []string{"calls", "empty_fragments"} {
		t.Run(schedule, func(t *testing.T) {
			a := newToolCallAssembler([]toolcall.Finalizer{newMandatoryExpansionFin()}, 0, mandatoryCatalog())
			for i := range 5000 {
				ev := lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: fmt.Sprint(i), ToolName: mandatoryToolName}
				if schedule == "empty_fragments" && i > 0 {
					ev.Kind = lipapi.EventToolCallArgsDelta
					ev.ToolCallID = "0"
				}
				_, err := a.ingest(t.Context(), ev, toolcall.Meta{})
				if err != nil {
					if !errors.Is(err, ErrToolCallBufferBudget) {
						t.Fatal(err)
					}
					return
				}
			}
			t.Fatal("unbounded call/fragment retention")
		})
	}
}

func TestToolCallAssembler_ReleasesActiveBudgetAndBoundsHistory(t *testing.T) {
	fin := newScopedFin(t, "budget", 0, toolcall.BufferingSpec{MaxArgsBytes: toolcall.MaxMandatoryMaxArgsBytes, Overflow: toolcall.OverflowReject}, mandatoryToolName)
	a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, mandatoryCatalog())
	// Sequential completion must not consume the concurrent-buffer allowance.
	for i := range lipapi.MaxItems {
		id := fmt.Sprint(i)
		for _, ev := range []lipapi.Event{
			{Kind: lipapi.EventToolCallStarted, ToolCallID: id, ToolName: mandatoryToolName},
			{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: id, Delta: `{}`},
			{Kind: lipapi.EventToolCallFinished, ToolCallID: id},
		} {
			if _, err := a.ingest(t.Context(), ev, toolcall.Meta{}); err != nil {
				t.Fatalf("call %d: %v", i, err)
			}
		}
		for {
			if _, ok := a.popDrain(); !ok {
				break
			}
		}
	}
	if _, err := a.ingest(t.Context(), lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "extra", ToolName: mandatoryToolName}, toolcall.Meta{}); !errors.Is(err, ErrToolCallBufferBudget) {
		t.Fatal("completed-identity history was unbounded")
	}
}

func TestToolCallAssembler_OverflowRefusalSurvivesMissingToolFinish(t *testing.T) {
	fin := newScopedFin(t, "overflow", 0, toolcall.BufferingSpec{MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes, Overflow: toolcall.OverflowReject}, mandatoryToolName)
	a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, mandatoryCatalog())
	for _, ev := range []lipapi.Event{
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: mandatoryToolName},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: strings.Repeat("x", toolcall.MinMandatoryMaxArgsBytes+1)},
	} {
		if _, err := a.ingest(t.Context(), ev, toolcall.Meta{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, err := range []error{a.completionError(), func() error {
		_, err := a.ingest(t.Context(), lipapi.Event{Kind: lipapi.EventResponseFinished}, toolcall.Meta{})
		return err
	}()} {
		var refusal *MandatoryBufferingError
		if !errors.As(err, &refusal) || refusal.Reason != ReasonMandatoryBufferingOverflow {
			t.Fatal("pending overflow refusal was lost")
		}
	}
}

func TestToolCallAssembler_InvalidObservationDeclarationCannotHideAtCompletion(t *testing.T) {
	fin := newScopedFin(t, "invalid", 0, toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
		Completeness: toolcall.CompletenessBestEffort,
	}, mandatoryToolName)
	a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, mandatoryCatalog())
	if _, err := a.ingest(t.Context(), lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: mandatoryToolName}, toolcall.Meta{}); err != nil {
		t.Fatal(err)
	}
	var refusal *MandatoryBufferingError
	if !errors.As(a.completionError(), &refusal) || refusal.Reason != ReasonMandatoryBufferingDeclarationInvalid {
		t.Fatal("invalid best-effort declaration disappeared at completion")
	}
}
