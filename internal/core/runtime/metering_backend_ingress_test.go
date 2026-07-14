package runtime

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestFreezeBackendIngress_RejectsWidening(t *testing.T) {
	t.Parallel()
	authorized := lipapi.Call{
		ID: "r",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("ok")},
		}},
	}
	snap, err := checkpoint.CaptureBackendIngress(checkpoint.BackendIngressInput{
		Call:         authorized,
		AttemptID:    "att",
		BLegID:       "b",
		CheckpointID: "cp",
		StreamID:     "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	wide := lipapi.CloneCall(authorized)
	wide.Messages[0].Parts[0].Text = "widened"
	if err := checkpoint.AssertNotWidened(snap.Call, wide); !errors.Is(err, checkpoint.ErrUnmeasuredWidening) {
		t.Fatalf("got %v", err)
	}
}
