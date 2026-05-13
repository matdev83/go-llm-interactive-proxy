package runtime

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestAttemptAccountingTracker_ignoresKeepaliveAndWhitespaceForTTFT(t *testing.T) {
	t.Parallel()

	start := time.Unix(100, 0)
	tr := newAttemptAccountingTracker(start)
	tr.observeBackendEvent(start.Add(100*time.Millisecond), lipapi.Event{
		Kind: lipapi.EventWarning, WarningCode: stream.KeepaliveEventCode,
	})
	tr.observeBackendEvent(start.Add(150*time.Millisecond), lipapi.Event{Kind: lipapi.EventResponseStarted})
	tr.observeClientEvent(start.Add(200*time.Millisecond), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "   \n"})
	tr.observeClientEvent(start.Add(350*time.Millisecond), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"})
	tr.observeClientEvent(start.Add(850*time.Millisecond), lipapi.Event{Kind: lipapi.EventResponseFinished})

	got := tr.snapshot()
	if !got.FirstRemoteEventAt.Equal(start.Add(150 * time.Millisecond)) {
		t.Fatalf("FirstRemoteEventAt: got %v", got.FirstRemoteEventAt)
	}
	if !got.FirstMeaningfulTokenAt.Equal(start.Add(350 * time.Millisecond)) {
		t.Fatalf("FirstMeaningfulTokenAt: got %v", got.FirstMeaningfulTokenAt)
	}
	if got.TTFTMillis != 350 {
		t.Fatalf("TTFTMillis: got %d want 350", got.TTFTMillis)
	}
}

func TestAttemptAccountingTracker_completionTPSUsesOutputTokensFromMeaningfulTokenToFinish(t *testing.T) {
	t.Parallel()

	start := time.Unix(200, 0)
	tr := newAttemptAccountingTracker(start)
	tr.observeClientEvent(start.Add(time.Second), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "x"})
	tr.observeUsage(lipapi.Event{Kind: lipapi.EventUsageDelta, OutputTokens: 10})
	tr.observeClientEvent(start.Add(3*time.Second), lipapi.Event{Kind: lipapi.EventResponseFinished})

	got := tr.snapshot()
	if got.CompletionTPSMilli != 5000 {
		t.Fatalf("CompletionTPSMilli: got %d want 5000", got.CompletionTPSMilli)
	}
	if got.CompletionDurationMillis != 2000 {
		t.Fatalf("CompletionDurationMillis: got %d want 2000", got.CompletionDurationMillis)
	}
}
