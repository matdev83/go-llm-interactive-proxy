package testkit

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// AssertEventCount asserts the number of canonical events in a non-streaming collection.
func AssertEventCount(t *testing.T, events []lipapi.Event, want int) {
	t.Helper()
	if len(events) != want {
		t.Fatalf("event count: want %d, got %d", want, len(events))
	}
}
