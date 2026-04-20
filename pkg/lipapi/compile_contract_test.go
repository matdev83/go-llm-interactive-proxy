package lipapi_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestCanonicalTypesAreConstructible(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("ping")},
		}},
	}
	if err := call.Validate(); err != nil {
		t.Fatal(err)
	}

	ev := lipapi.Event{Kind: lipapi.EventResponseStarted}
	_ = ev

	cap := lipapi.CapabilitySet{Provides: []lipapi.Capability{lipapi.CapabilityStreaming}}
	_ = cap
}
