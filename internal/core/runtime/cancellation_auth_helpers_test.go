package runtime_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func captureAuthoritativeID() (chan string, func(lipapi.Call)) {
	ch := make(chan string, 1)
	send := func(call lipapi.Call) {
		select {
		case ch <- call.Session.ALegID:
		default:
		}
	}
	return ch, send
}

func requireAuthoritativeID(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case id := <-ch:
		if id == "" {
			t.Fatalf("authoritative ALegID was empty")
		}
		return id
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for authoritative ALegID")
		return ""
	}
}
