package lipapi_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestAttemptRecord_roundTripFields(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	rec := lipapi.AttemptRecord{
		BLegID:         "b1",
		ALegID:         "a1",
		Seq:            2,
		BackendID:      "openairesponses",
		EffectiveModel: "gpt-4",
		StartedAt:      start,
		FinishedAt:     end,
		Outcome:        lipapi.AttemptSwallowedFailure,
		Reason:         "upstream_timeout_pre_output",
	}
	if rec.Outcome != lipapi.AttemptSwallowedFailure {
		t.Fatalf("outcome: got %q", rec.Outcome)
	}
	if rec.Seq != 2 {
		t.Fatalf("seq: got %d", rec.Seq)
	}
}
