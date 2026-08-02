package openresponses

import (
	"net"
	"testing"
	"time"
)

type round5TimeoutError struct{}

func (round5TimeoutError) Error() string   { return "round5 idle timeout" }
func (round5TimeoutError) Timeout() bool   { return true }
func (round5TimeoutError) Temporary() bool { return true }

var _ net.Error = round5TimeoutError{}

func TestRound5PollReadTerminationWaitsAfterPeerClosed(t *testing.T) {
	done := make(chan sessionPumpResult, 1)
	peerClosed := make(chan struct{})
	close(peerClosed)
	go func() {
		time.Sleep(2 * time.Millisecond)
		done <- sessionPumpResult{fromRead: true, err: round5TimeoutError{}}
	}()

	result, ok := pollReadTermination(done, peerClosed)
	if !ok {
		t.Fatal("pollReadTermination returned no result after peer close")
	}
	if !result.fromRead || !isReadTimeout(result.err) {
		t.Fatalf("result = %#v, want delayed read timeout", result)
	}
}
