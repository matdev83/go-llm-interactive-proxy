package openaiusage

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestNativeUsageMeasuresPreserveMultimodalDirectionAndUnits(t *testing.T) {
	raw := `{"input_image_tokens":0,"output_image_tokens":7,"input_audio_seconds":1.5,"output_video_frames":12,"input_document_pages":2,"output_bytes":4096}`
	measures := NativeUsageMeasures(raw)
	if len(measures) != 6 {
		t.Fatalf("native measures = %d, want 6", len(measures))
	}
	want := map[string]struct {
		direction sdkmetering.FlowDirection
		unit      string
		value     string
		scale     uint8
	}{
		sdkmetering.ComponentImageToken:             {sdkmetering.DirectionInput, sdkmetering.UnitToken, "0", 0},
		sdkmetering.ComponentImageToken + ":output": {sdkmetering.DirectionOutput, sdkmetering.UnitToken, "7", 0},
		sdkmetering.ComponentAudio:                  {sdkmetering.DirectionInput, sdkmetering.UnitSecond, "15", 1},
		sdkmetering.ComponentVideo + ":output":      {sdkmetering.DirectionOutput, sdkmetering.UnitFrame, "12", 0},
		sdkmetering.ComponentDocument:               {sdkmetering.DirectionInput, sdkmetering.UnitPage, "2", 0},
		sdkmetering.ComponentFile + ":output":       {sdkmetering.DirectionOutput, sdkmetering.UnitByte, "4096", 0},
	}
	for _, measure := range measures {
		key := measure.Key.Component
		if measure.Key.Direction == sdkmetering.DirectionOutput {
			key += ":output"
		}
		expected, ok := want[key]
		if !ok {
			t.Errorf("unexpected measure key: %+v", measure.Key)
			continue
		}
		if measure.Key.Direction != expected.direction || measure.Key.Unit != expected.unit || measure.Value == nil || measure.Value.Coefficient != expected.value || measure.Value.Scale != expected.scale {
			t.Errorf("measure %s = %+v, want direction=%s unit=%s value=%s scale=%d", key, measure, expected.direction, expected.unit, expected.value, expected.scale)
		}
	}
}

func TestNativeUsageMeasuresRejectMalformedNegativeAndFractionalDiscreteValues(t *testing.T) {
	raw := `{"input_image_tokens":-1,"output_video_frames":1.2,"input_audio_seconds":"oops","output_image_count":1e999}`
	if got := NativeUsageMeasures(raw); len(got) != 0 {
		t.Fatalf("invalid native values should be unavailable, got %d measures", len(got))
	}
}

func TestNativeUsageEvidenceRetainsPresentZeroOncePerSafePath(t *testing.T) {
	raw := `{"input_image_tokens":0,"output_image_tokens":7,"input_audio_seconds":0}`
	evidence := NativeUsageEvidence(raw)
	if len(evidence) != 3 {
		t.Fatalf("native evidence = %d, want 3", len(evidence))
	}
	for _, field := range evidence {
		if !field.Present || field.Lexeme == "" {
			t.Errorf("evidence lost presence/lexeme: %+v", field)
		}
	}
}

func TestNativeUsageEvidenceRejectsMalformedNegativeAndFractionalDiscreteValues(t *testing.T) {
	raw := `{"input_image_tokens":-1,"output_video_frames":1.2,"input_audio_seconds":"oops","output_image_count":1e999}`
	if got := NativeUsageEvidence(raw); len(got) != 0 {
		t.Fatalf("invalid native evidence should be unavailable, got %d fields", len(got))
	}
}

func TestNativeUsageMeasuresMapsOpenResponsesDetailDirections(t *testing.T) {
	raw := `{"input_tokens_details":{"text_tokens":4,"audio_tokens":3,"images":0},"output_tokens_details":{"text_tokens":6,"audio_tokens":5,"images":2}}`
	measures := NativeUsageMeasures(raw)
	if len(measures) != 6 {
		t.Fatalf("detail measures = %d, want 6", len(measures))
	}
	want := map[string]string{
		"input/text_token":   "4",
		"output/text_token":  "6",
		"input/audio_token":  "3",
		"output/audio_token": "5",
		"input/image":        "0",
		"output/image":       "2",
	}
	for _, measure := range measures {
		key := string(measure.Key.Direction) + "/" + measure.Key.Component
		if got := want[key]; got == "" {
			t.Errorf("unexpected detail measure: %+v", measure.Key)
		} else if measure.Value == nil || measure.Value.Coefficient != got {
			t.Errorf("detail measure %s = %+v, want %s", key, measure.Value, got)
		}
	}
}

func TestProviderEvidenceDraftRetainsGenuineProviderCostAndRawLexeme(t *testing.T) {
	event := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		CostNanoUnits: 140_000,
		Currency:      "USD",
		CostPresent:   true,
		RawUsageJSON:  `{"input_tokens":3,"cost":0.00014}`,
		Accounting: lipapi.UsageAccountingMetadata{
			Source: lipapi.UsageSourceProviderReported,
		},
	}
	draft := ProviderEvidenceDraft(event, "openai.chat.v2", "openai.chat:usage")
	if len(draft.Charges) != 1 || draft.Charges[0].Amount == nil {
		t.Fatalf("provider cost charge = %+v, want one amount", draft.Charges)
	}
	if got := draft.Charges[0].Amount.CanonicalString(); got != "14/5" {
		t.Fatalf("provider cost amount = %q, want 14/5", got)
	}
	if got := draft.Charges[0].Currency; got != "USD" {
		t.Fatalf("provider cost currency = %q, want USD", got)
	}
	var amount, currency sdkmetering.SafeEvidenceField
	for _, field := range draft.Evidence {
		switch field.Path {
		case "$.cost.amount":
			amount = field
		case "$.cost.currency":
			currency = field
		}
	}
	if amount.Lexeme != "0.00014" || !amount.Present || currency.Lexeme != "USD" || !currency.Present {
		t.Fatalf("provider cost evidence = amount=%+v currency=%+v", amount, currency)
	}
}

func TestProviderEvidenceDraftRejectsMalformedOrNegativeProviderCost(t *testing.T) {
	for _, raw := range []string{`{"cost":-1}`, `{"cost":1e100}`, `{"cost":"not-a-number"}`} {
		event := lipapi.Event{
			Kind:          lipapi.EventUsageDelta,
			CostNanoUnits: 1,
			Currency:      "USD",
			CostPresent:   true,
			RawUsageJSON:  raw,
			Accounting: lipapi.UsageAccountingMetadata{
				Source: lipapi.UsageSourceProviderReported,
			},
		}
		draft := ProviderEvidenceDraft(event, "openai.chat.v2", "openai.chat:usage")
		if len(draft.Charges) != 0 {
			t.Errorf("raw cost %s produced charge %+v", raw, draft.Charges)
		}
	}
}
