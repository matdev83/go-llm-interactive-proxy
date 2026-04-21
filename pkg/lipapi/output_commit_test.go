package lipapi_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestOutputCommitted(t *testing.T) {
	t.Parallel()
	if !lipapi.OutputCommitted(lipapi.Event{Kind: lipapi.EventTextDelta}) {
		t.Fatal("text delta commits")
	}
	if !lipapi.OutputCommitted(lipapi.Event{Kind: lipapi.EventToolCallStarted}) {
		t.Fatal("tool start commits")
	}
	if lipapi.OutputCommitted(lipapi.Event{Kind: lipapi.EventResponseStarted}) {
		t.Fatal("response_started does not commit for failover")
	}
	if lipapi.OutputCommitted(lipapi.Event{Kind: lipapi.EventMessageStarted}) {
		t.Fatal("message_started does not commit for failover")
	}
	if !lipapi.OutputCommitted(lipapi.Event{Kind: lipapi.EventAssistantImageRef}) {
		t.Fatal("assistant_image_ref commits")
	}
}
