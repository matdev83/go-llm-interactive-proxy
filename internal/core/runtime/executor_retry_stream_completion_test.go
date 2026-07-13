package runtime

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestGateBufHasCommittedOutput(t *testing.T) {
	t.Parallel()
	if gateBufHasCommittedOutput(nil) {
		t.Fatal("nil buffer")
	}
	if gateBufHasCommittedOutput([]lipapi.Event{{Kind: lipapi.EventResponseStarted}}) {
		t.Fatal("lifecycle only")
	}
	if !gateBufHasCommittedOutput([]lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "x"}}) {
		t.Fatal("text delta commits")
	}
}
