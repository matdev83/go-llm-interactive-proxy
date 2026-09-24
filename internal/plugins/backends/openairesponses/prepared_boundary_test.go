package openairesponses

import (
	"context"
	"testing"

	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestObservePreparedInputUsesFinalResponsesPayloadFields(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{Messages: []lipapi.Message{{
		Role: lipapi.RoleUser,
		Parts: []lipapi.Part{
			lipapi.TextPart("describe"),
			lipapi.FilePart("data:application/pdf;base64,QUJD", "application/pdf", "final.pdf"),
		},
	}}}
	params, err := ParamsForCall(&call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-test"}})
	if err != nil {
		t.Fatalf("ParamsForCall: %v", err)
	}
	canonical := coremetering.PreparedInputSummaryFromCall(call)
	var observed coremetering.PreparedInputSummary
	ctx := coremetering.WithPreparedInputObserver(context.Background(), func(summary coremetering.PreparedInputSummary) {
		observed = summary
	})
	observePreparedInput(ctx, params)
	if observed.MethodRef != openAIResponsesPreparedInputMethod {
		t.Fatalf("method=%q, want %q", observed.MethodRef, openAIResponsesPreparedInputMethod)
	}
	if len(observed.Media) != 1 || observed.Media[0].Kind != coremetering.MediaDocument || !observed.Media[0].BytesPresent || observed.Media[0].Bytes != 4 {
		t.Fatalf("final provider media=%#v, want final file field bytes", observed.Media)
	}
	if len(canonical.Media) != 1 || canonical.Media[0].BytesPresent {
		t.Fatalf("canonical ingress media changed or unexpectedly has bytes: %#v", canonical.Media)
	}
	if observed.TextBytes != canonical.TextBytes {
		t.Fatalf("text bytes changed unexpectedly: final=%d canonical=%d", observed.TextBytes, canonical.TextBytes)
	}
}
