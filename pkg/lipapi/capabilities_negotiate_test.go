package lipapi_test

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNegotiate_lossless(t *testing.T) {
	t.Parallel()

	res := lipapi.Negotiate(
		[]lipapi.Capability{lipapi.CapabilityTools, lipapi.CapabilityStreaming},
		lipapi.NewBackendCaps(lipapi.CapabilityTools, lipapi.CapabilityStreaming, lipapi.CapabilityVision),
	)
	if res.Kind != lipapi.NegotiationLossless {
		t.Fatalf("got %s", res.Kind)
	}
	if res.Err() != nil {
		t.Fatalf("unexpected err: %v", res.Err())
	}
}

func TestNegotiate_downgradeReasoningAndParallelTools(t *testing.T) {
	t.Parallel()

	par := true
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "t"}},
		Options: lipapi.GenerationOptions{
			ReasoningEffort:   "high",
			ParallelToolCalls: &par,
		},
	}
	required := lipapi.RequiredCapabilities(call)

	backend := lipapi.NewBackendCaps(lipapi.CapabilityTools)
	res := lipapi.Negotiate(required, backend)
	if res.Kind != lipapi.NegotiationDowngrade {
		t.Fatalf("got %s missing=%v down=%v", res.Kind, res.Missing, res.Downgraded)
	}
	if len(res.Downgraded) == 0 {
		t.Fatal("expected downgraded list")
	}
}

func TestNegotiate_rejectMissingVision(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind:     lipapi.PartImageRef,
				ImageRef: "https://example.com/x.png",
			}},
		}},
	}
	required := lipapi.RequiredCapabilities(call)

	res := lipapi.Negotiate(required, lipapi.NewBackendCaps(lipapi.CapabilityTools))
	if res.Kind != lipapi.NegotiationReject {
		t.Fatalf("got %s", res.Kind)
	}
	if len(res.Missing) == 0 {
		t.Fatal("expected missing capabilities")
	}

	err := res.Err()
	if err == nil {
		t.Fatal("expected reject error")
	}
	if !errors.Is(err, lipapi.ErrCapabilityReject) {
		t.Fatalf("expected ErrCapabilityReject wrap: %v", err)
	}
	if !lipapi.IsReject(err) {
		t.Fatal("expected IsReject")
	}
}

func TestRequiredCapabilities_mapsCallShape(t *testing.T) {
	t.Parallel()

	par := true
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind:     lipapi.PartImageRef,
				ImageRef: "x",
			}},
		}},
		Tools: []lipapi.ToolDef{{Name: "t"}},
		Options: lipapi.GenerationOptions{
			ResponseMIMEType:  "application/json",
			ReasoningEffort:   "low",
			ParallelToolCalls: &par,
		},
	}

	got := lipapi.RequiredCapabilities(call)
	want := map[lipapi.Capability]struct{}{
		lipapi.CapabilityVision:            {},
		lipapi.CapabilityTools:             {},
		lipapi.CapabilityStructuredOutputs: {},
		lipapi.CapabilityReasoning:         {},
		lipapi.CapabilityParallelToolCalls: {},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for _, c := range got {
		if _, ok := want[c]; !ok {
			t.Fatalf("unexpected capability %q", c)
		}
	}
}

func TestNegotiate_nilBackendTreatsAsEmpty(t *testing.T) {
	t.Parallel()

	res := lipapi.Negotiate([]lipapi.Capability{lipapi.CapabilityTools}, nil)
	if res.Kind != lipapi.NegotiationReject {
		t.Fatalf("got %s", res.Kind)
	}
}

func TestApplyNegotiatedDowngrades_stripsSoftCapabilities(t *testing.T) {
	t.Parallel()
	par := true
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Options: lipapi.GenerationOptions{
			ReasoningEffort:   "high",
			ParallelToolCalls: &par,
		},
	}
	res := lipapi.Negotiate(lipapi.RequiredCapabilities(call), lipapi.NewBackendCaps(lipapi.CapabilityStreaming))
	if res.Kind != lipapi.NegotiationDowngrade {
		t.Fatalf("got %s", res.Kind)
	}
	lipapi.ApplyNegotiatedDowngrades(&call, res)
	if call.Options.ReasoningEffort != "" {
		t.Fatalf("reasoning: %q", call.Options.ReasoningEffort)
	}
	if call.Options.ParallelToolCalls != nil {
		t.Fatal("expected parallel tool calls cleared")
	}
}

func TestRequiredCapabilities_fileRefRequiresDocuments(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.FilePart("file-123", "application/pdf", "doc.pdf"),
			},
		}},
	}
	got := lipapi.RequiredCapabilities(call)
	found := false
	for _, c := range got {
		if c == lipapi.CapabilityDocuments {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected CapabilityDocuments in %v", got)
	}
}

func TestNegotiate_rejectMissingDocuments(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.FilePart("file-123", "application/pdf", "doc.pdf"),
			},
		}},
	}
	res := lipapi.Negotiate(lipapi.RequiredCapabilities(call), lipapi.NewBackendCaps(lipapi.CapabilityStreaming))
	if res.Kind != lipapi.NegotiationReject {
		t.Fatalf("got %s", res.Kind)
	}
}
