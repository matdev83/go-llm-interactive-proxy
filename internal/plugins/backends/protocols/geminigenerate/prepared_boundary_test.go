package geminigenerate

import (
	"context"
	"testing"

	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestObservePreparedInputUsesDecodedGeminiProviderRepresentation(t *testing.T) {
	call := lipapi.Call{Messages: []lipapi.Message{{
		Role: lipapi.RoleUser,
		Parts: []lipapi.Part{
			lipapi.TextPart("describe"),
			{Kind: lipapi.PartImageRef, ImageRef: "data:image/png;base64,QUJD", ImageMIME: "image/png"},
		},
	}}}
	params, err := StreamParamsForCall(&call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-test"}})
	if err != nil {
		t.Fatalf("StreamParamsForCall: %v", err)
	}
	canonical := coremetering.PreparedInputSummaryFromCall(call)
	var observed coremetering.PreparedInputSummary
	ctx := coremetering.WithPreparedInputObserver(context.Background(), func(summary coremetering.PreparedInputSummary) {
		observed = summary
	})
	observePreparedInput(ctx, params)
	if observed.MethodRef != geminiPreparedInputMethod {
		t.Fatalf("method=%q, want %q", observed.MethodRef, geminiPreparedInputMethod)
	}
	if len(observed.Media) != 1 || observed.Media[0].Kind != coremetering.MediaImage || !observed.Media[0].BytesPresent || observed.Media[0].Bytes != 3 {
		t.Fatalf("final provider media=%#v, want decoded image bytes", observed.Media)
	}
	if len(canonical.Media) != 1 || canonical.Media[0].BytesPresent {
		t.Fatalf("canonical ingress media changed or unexpectedly has bytes: %#v", canonical.Media)
	}
	if observed.TextBytes != canonical.TextBytes {
		t.Fatalf("text bytes changed unexpectedly: final=%d canonical=%d", observed.TextBytes, canonical.TextBytes)
	}
}
